package main

import (
	"flag"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alicebob/miniredis"
	"github.com/flashbots/prio-load-balancer/server"
	"github.com/flashbots/prio-load-balancer/testutils"
	"go.uber.org/zap"

	ratls "github.com/konvera/gramine-ratls-golang"
)

var (
	version = "dev" // is set during build process

	// Default values
	// defaultDebug       = os.Getenv("DEBUG") == "1"
	defaultRedis       = getEnv("REDIS_URI", "dev")
	defaultListenAddr  = getEnv("LISTEN_ADDR", "localhost:8080")
	defaultlogProd     = os.Getenv("LOG_PROD") == "1"
	defaultLogService  = os.Getenv("LOG_SERVICE")
	defaultNodeWorkers = getEnvInt("NUM_NODE_WORKERS", 8) // number of maximum concurrent requests per node
	defaultNodes       = os.Getenv("NODES")
	defaultBackends    = os.Getenv("BACKENDS")

	// Flags
	httpAddrPtr = flag.String("http", defaultListenAddr, "http service address")
	// debugPtr       = flag.Bool("debug", defaultDebug, "print debug output")
	nodeWorkersPtr = flag.Int("node-workers", defaultNodeWorkers, "number of concurrent workers per node")
	nodesPtr       = flag.String("nodes", defaultNodes, "nodes to use (comma separated)")
	backendsPtr    = flag.String("backends", defaultBackends, "backend nodes to use (comma separated URLs to proxy requests to)")
	redisPtr       = flag.String("redis", defaultRedis, "redis URI ('dev' for built-in)")
	useMockNodePtr = flag.Bool("mock-node", false, "run a mock node backend")
	logProdPtr     = flag.Bool("log-prod", defaultlogProd, "production logging")
	logServicePtr  = flag.String("log-service", defaultLogService, "'service' tag to logs")
)

func perr(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	flag.Parse()

	// Setup logging
	var logger *zap.Logger
	if *logProdPtr {
		logger, _ = zap.NewProduction()
	} else {
		logger, _ = zap.NewDevelopment()
	}
	log := logger.Sugar()
	if *logServicePtr != "" {
		log = log.With("service", *logServicePtr)
	}
	log.Infow("Starting prio-load-balancer", "version", version)

	// Setup the redis connection
	if *redisPtr == "dev" {
		log.Info("Using integrated in-memory Redis instance")
		redisServer, err := miniredis.Run()
		perr(err)
		*redisPtr = redisServer.Addr()
	}

	serverOpts := server.ServerOpts{
		Log:            log,
		RedisURI:       *redisPtr,
		WorkersPerNode: int32(*nodeWorkersPtr),
		HTTPAddrPtr:    *httpAddrPtr,
	}

	srv, err := server.NewServer(serverOpts)
	perr(err)

	// Initialise RATLS library
	err = ratls.InitRATLSLib(true, time.Hour, false)
	perr(err)

	if *useMockNodePtr {
		addr := "localhost:8095"
		mockNodeBackend := testutils.NewMockNodeBackend()
		http.HandleFunc("/", mockNodeBackend.Handler)
		log.Info("Using mock node backend", "listenAddr", addr)
		go http.ListenAndServe(addr, nil)
		perr(srv.AddNode("http://" + addr))
	}

	if *nodesPtr != "" {
		for _, uri := range strings.Split(*nodesPtr, ",") {
			perr(srv.AddNode(uri))
		}
	}

	if *backendsPtr != "" {
		for _, uri := range strings.Split(*backendsPtr, ",") {
			perr(srv.AddNode(uri))
		}
	}

	go func() { // All 10 seconds: log stats
		for {
			time.Sleep(10 * time.Second)
			log.Infow("goroutines:", "numGoroutines", runtime.NumGoroutine())
			lenHighPrio, lenLowPrio := srv.QueueSize()
			log.Infow("prioQueue size:", "highPrio", lenHighPrio, "lowPrio", lenLowPrio)
		}
	}()

	// Handle shutdown gracefully
	go func() {
		exit := make(chan os.Signal, 1)
		signal.Notify(exit, os.Interrupt, syscall.SIGTERM)
		<-exit
		log.Info("Shutting down...")
		srv.Shutdown()
	}()

	// Log the current config
	server.LogConfig(log)

	// Start the server
	srv.Start()
	log.Info("bye")
}

func getEnv(key string, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value, ok := os.LookupEnv(key); ok {
		val, err := strconv.Atoi(value)
		if err == nil {
			return val
		}
	}
	return defaultValue
}
