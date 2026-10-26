package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestDeleteFinishedRequestsBeforeMariaDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is required for MariaDB retention integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MariaDB: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping MariaDB: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&databaseName); err != nil {
		t.Fatalf("read selected database: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(databaseName), "_test") {
		t.Fatalf("refusing retention integration test against non-test database %q", databaseName)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source file for migration schema")
	}
	schema, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "../../migrations/001_init.sql"))
	if err != nil {
		t.Fatalf("read migration schema: %v", err)
	}
	for _, statement := range strings.Split(string(schema), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply test schema: %v", err)
		}
	}

	prefix := fmt.Sprintf("retention-%d", time.Now().UnixNano())
	expired1, expired2 := prefix+"-old-1", prefix+"-old-2"
	running, recent := prefix+"-running", prefix+"-recent"
	ids := []string{expired1, expired2, running, recent}
	defer func() {
		for _, table := range []string{"request_stream_events", "request_payloads", "request_logs"} {
			_, _ = db.ExecContext(context.Background(), "DELETE FROM "+table+" WHERE request_id LIKE ?", prefix+"%")
		}
	}()
	old := time.Now().Add(-72 * time.Hour)
	recentTime := time.Now().Add(-2 * time.Hour)
	insertLog := func(id, status string, started time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "INSERT INTO request_logs(request_id,endpoint,method,status,started_at) VALUES(?,?,?,?,?)", id, "/v1/chat/completions", "POST", status, started); err != nil {
			t.Fatalf("insert request log: %v", err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO request_payloads(request_id,created_at) VALUES(?,?)", id, time.Now()); err != nil {
			t.Fatalf("insert request payload: %v", err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO request_stream_events(request_id,seq,at_ms,kind) VALUES(?,?,?,?)", id, 1, 1, "test"); err != nil {
			t.Fatalf("insert stream event: %v", err)
		}
	}
	insertLog(expired1, "completed", old)
	insertLog(expired2, "error", old.Add(time.Minute))
	insertLog(running, "running", old)
	insertLog(recent, "completed", recentTime)

	repo := NewRepo(db)
	cutoff := time.Now().Add(-24 * time.Hour)
	var deleted int64
	for i := 0; i < 4; i++ {
		n, err := repo.DeleteFinishedRequestsBefore(ctx, cutoff, 1)
		if err != nil {
			t.Fatalf("delete expired batch %d: %v", i, err)
		}
		deleted += n
		if n == 0 {
			break
		}
	}
	if deleted != 2 {
		t.Fatalf("deleted request count = %d, want 2", deleted)
	}
	for _, tc := range []struct {
		id   string
		want int
	}{
		{expired1, 0},
		{expired2, 0},
		{running, 1},
		{recent, 1},
	} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM request_logs WHERE request_id=?", tc.id).Scan(&count); err != nil {
			t.Fatalf("count request %q: %v", tc.id, err)
		}
		if count != tc.want {
			t.Fatalf("request %q remains %d times, want %d", tc.id, count, tc.want)
		}
		for _, table := range []string{"request_payloads", "request_stream_events"} {
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE request_id=?", tc.id).Scan(&count); err != nil {
				t.Fatalf("count %s for %q: %v", table, tc.id, err)
			}
			wantChildren := tc.want
			if tc.id == running || tc.id == recent {
				wantChildren = 1
			}
			if count != wantChildren {
				t.Fatalf("%s rows for %q = %d, want %d", table, tc.id, count, wantChildren)
			}
		}
	}
	_ = ids
}
