//go:build windows

package storage

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const mysqlHealMinInterval = 15 * time.Second

var (
	mysqlHealMu          sync.Mutex
	mysqlHealLastAttempt time.Time
	mysqlHealLastService string
	mysqlHealLastState   string
)

func candidateMySQLServices() []string {
	if custom := strings.TrimSpace(os.Getenv("CLIPSYNC_MYSQL_SERVICE")); custom != "" {
		return []string{custom}
	}
	return []string{"MySQL80", "MySQL84", "MySQL90", "MySQL", "MySQL57", "MariaDB"}
}

func healLocalMySQLPlatform(ctx context.Context) MySQLServiceStatus {
	for _, svc := range candidateMySQLServices() {
		out, exitCode, err := runSCCommand(ctx, 2*time.Second, "query", svc)
		if exitCode == 1060 || (err != nil && out == "") {
			continue
		}
		state := parseSCServiceState(out)
		if state == "" {
			continue
		}
		if state == "running" || state == "start_pending" {
			mysqlHealMu.Lock()
			if mysqlHealLastService == svc {
				mysqlHealLastState = state
			}
			mysqlHealMu.Unlock()
			return MySQLServiceStatus{ServiceName: svc, State: state}
		}
		if state != "stopped" && state != "stop_pending" {
			return MySQLServiceStatus{ServiceName: svc, State: state}
		}

		mysqlHealMu.Lock()
		now := time.Now()
		if mysqlHealLastService == svc && !mysqlHealLastAttempt.IsZero() && now.Sub(mysqlHealLastAttempt) < mysqlHealMinInterval && mysqlHealLastState != "" {
			cached := mysqlHealLastState
			mysqlHealMu.Unlock()
			return MySQLServiceStatus{ServiceName: svc, State: cached}
		}
		mysqlHealLastAttempt = now
		mysqlHealLastService = svc
		mysqlHealMu.Unlock()

		_, startExit, startErr := runSCCommand(ctx, 4*time.Second, "start", svc)
		var resultState string
		switch {
		case startErr == nil || startExit == 0 || startExit == 1056:
			log.Printf("local MySQL self-heal started Windows service %s", svc)
			resultState = "start_pending"
		case startExit == 5:
			log.Printf("local MySQL service %s is stopped; elevation required to start (access denied)", svc)
			resultState = "access_denied"
		default:
			log.Printf("local MySQL service %s start attempt failed (exit=%d): %v", svc, startExit, startErr)
			resultState = "stopped"
		}

		mysqlHealMu.Lock()
		mysqlHealLastState = resultState
		mysqlHealMu.Unlock()
		return MySQLServiceStatus{ServiceName: svc, State: resultState}
	}
	return MySQLServiceStatus{}
}

func runSCCommand(parent context.Context, timeout time.Duration, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sc.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return string(output), exitCode, err
}
