package storage

import (
	"context"
	"net"
	"os"
	"regexp"
	"strings"
)

type MySQLServiceStatus struct {
	ServiceName string `json:"service_name,omitempty"`
	State       string `json:"state,omitempty"`
}

var (
	dsnTCPPattern  = regexp.MustCompile(`@tcp\(([^)]*)\)`)
	scStatePattern = regexp.MustCompile(`(?m)^\s*STATE\s*:\s*([1-7])\b`)
)

func HealLocalMySQLFromEnv(ctx context.Context) MySQLServiceStatus {
	if !IsLocalMySQLTargetFromEnv() {
		return MySQLServiceStatus{}
	}
	return healLocalMySQLPlatform(ctx)
}

func IsLocalMySQLTargetFromEnv() bool {
	if dsn := strings.TrimSpace(os.Getenv("CLIPSYNC_MYSQL_DSN")); dsn != "" {
		return isLocalMySQLDSN(dsn)
	}
	addr := strings.TrimSpace(firstEnv("CLIPSYNC_MYSQL_ADDR", "MYSQL_ADDR"))
	if addr == "" {
		return true
	}
	return isLocalHostAddr(addr)
}

func isLocalMySQLDSN(dsn string) bool {
	if strings.Contains(dsn, "@unix(") {
		return true
	}
	m := dsnTCPPattern.FindStringSubmatch(dsn)
	if len(m) < 2 {
		return !strings.Contains(dsn, "@")
	}
	target := strings.TrimSpace(m[1])
	if target == "" {
		return true
	}
	return isLocalHostAddr(target)
}

func isLocalHostAddr(addr string) bool {
	host := strings.TrimSpace(addr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	} else {
		host = strings.Trim(host, "[]")
	}
	host = strings.TrimSpace(host)
	return host == "" ||
		strings.EqualFold(host, "localhost") ||
		host == "127.0.0.1" ||
		host == "::1"
}

func parseSCServiceState(output string) string {
	m := scStatePattern.FindStringSubmatch(output)
	if len(m) < 2 {
		return ""
	}
	switch m[1] {
	case "1":
		return "stopped"
	case "2":
		return "start_pending"
	case "3":
		return "stop_pending"
	case "4":
		return "running"
	case "5", "6", "7":
		return "paused"
	default:
		return ""
	}
}
