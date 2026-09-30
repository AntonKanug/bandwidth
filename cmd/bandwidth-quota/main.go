package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/antonk/bandwidth-quota-service/internal/server"
)

const gracefulStopTimeout = 10 * time.Second

func main() {
	listen := flag.String("listen", ":9300", "gRPC listen address")
	redisAddr := flag.String("redis-addr", "127.0.0.1:6379", "Redis backend address")
	flag.Parse()

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}

	srv := server.NewRedisServer(*redisAddr)

	gs := grpc.NewServer()
	server.Register(gs, srv)
	healthpb.RegisterHealthServer(gs, health.NewServer())

	go func() {
		log.Printf("bandwidth-quota listening on %s, redis=%s", *listen, *redisAddr)
		if err := gs.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("shutdown signal received, draining...")

	// GracefulStop blocks indefinitely if a streaming client (e.g. the gRPC
	// Health watch) is hanging. Bound it so SIGTERM → process exit happens
	// well before the orchestrator's SIGKILL grace period.
	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		log.Print("graceful shutdown complete")
	case <-time.After(gracefulStopTimeout):
		log.Print("graceful shutdown timeout exceeded, forcing stop")
		gs.Stop()
	}
}
