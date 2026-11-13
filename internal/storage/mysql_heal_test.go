package storage

import "testing"

func TestIsLocalMySQLTarget(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		addr string
		want bool
	}{
		{name: "default empty", want: true},
		{name: "loopback ipv4", addr: "127.0.0.1:3306", want: true},
		{name: "localhost", addr: "localhost:3306", want: true},
		{name: "loopback ipv6", addr: "[::1]:3306", want: true},
		{name: "remote ipv4", addr: "10.0.0.12:3306", want: false},
		{name: "dsn local tcp", dsn: "root:pwd@tcp(127.0.0.1:3306)/clipsync_pro?parseTime=true", want: true},
		{name: "dsn empty tcp", dsn: "root:pwd@tcp()/clipsync_pro", want: true},
		{name: "dsn remote tcp", dsn: "root:pwd@tcp(db.example.internal:3306)/clipsync_pro", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLIPSYNC_MYSQL_DSN", tc.dsn)
			t.Setenv("CLIPSYNC_MYSQL_ADDR", tc.addr)
			t.Setenv("MYSQL_ADDR", "")
			if got := IsLocalMySQLTargetFromEnv(); got != tc.want {
				t.Fatalf("IsLocalMySQLTargetFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSCServiceState(t *testing.T) {
	stopped := "\r\nSERVICE_NAME: MySQL80 \r\n        TYPE               : 10  WIN32_OWN_PROCESS  \r\n        STATE              : 1  STOPPED \r\n"
	if got := parseSCServiceState(stopped); got != "stopped" {
		t.Fatalf("state = %q, want stopped", got)
	}
	running := "SERVICE_NAME: MySQL80\n        STATE              : 4  RUNNING\n"
	if got := parseSCServiceState(running); got != "running" {
		t.Fatalf("state = %q, want running", got)
	}
}
