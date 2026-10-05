package health

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres checks that the runtime role can reach the database.
func Postgres(pool *pgxpool.Pool) Check {
	return Check{Name: "postgres", Required: true, Probe: func(ctx context.Context) error {
		return pool.Ping(ctx)
	}}
}

// Redis checks the server with AUTH and PING over RESP.
func Redis(addr, password string) Check {
	return Check{Name: "redis", Required: true, Probe: func(ctx context.Context) error {
		return redisPing(ctx, addr, password)
	}}
}

// ObjectStore checks that the scoped credential can reach its bucket.
func ObjectStore(client *s3.Client, bucket string) Check {
	return Check{Name: "object_store", Required: true, Probe: func(ctx context.Context) error {
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket})
		return err
	}}
}

// Intelligence reports the optional internal intelligence service. It never
// gates core readiness.
func Intelligence(baseURL string) Check {
	client := &http.Client{Timeout: 2 * time.Second}
	return Check{Name: "intelligence", Required: false, Probe: func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}}
}

// redisPing speaks just enough RESP for AUTH and PING.
func redisPing(ctx context.Context, addr, password string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	r := bufio.NewReader(conn)
	send := func(args ...string) (string, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, a := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
		}
		if _, err := conn.Write([]byte(b.String())); err != nil {
			return "", err
		}
		line, err := r.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	if password != "" {
		reply, err := send("AUTH", password)
		if err != nil {
			return err
		}
		if reply != "+OK" {
			return errors.New("redis authentication failed")
		}
	}
	reply, err := send("PING")
	if err != nil {
		return err
	}
	if reply != "+PONG" {
		return errors.New("unexpected redis reply")
	}
	return nil
}
