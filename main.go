package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/breez/lspd/chain"
	"github.com/breez/lspd/cln"
	"github.com/breez/lspd/config"
	"github.com/breez/lspd/interceptor"
	"github.com/breez/lspd/lnd"
	"github.com/breez/lspd/mempool"
	"github.com/breez/lspd/postgresql"
	"github.com/btcsuite/btcd/btcec/v2"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "genkey" {
		p, err := btcec.NewPrivateKey()
		if err != nil {
			log.Fatalf("btcec.NewPrivateKey() error: %v", err)
		}
		fmt.Printf("LSPD_PRIVATE_KEY=\"%x\"\n", p.Serialize())
		return
	}

	n := os.Getenv("NODES")
	var nodes []*config.NodeConfig
	err := json.Unmarshal([]byte(n), &nodes)
	if err != nil {
		log.Fatalf("failed to unmarshal NODES env: %v", err)
	}

	if len(nodes) == 0 {
		log.Fatalf("need at least one node configured in NODES.")
	}

	var feeEstimator chain.FeeEstimator
	var feeStrategy chain.FeeStrategy
	useMempool := os.Getenv("USE_MEMPOOL_FEE_ESTIMATION") == "true"
	if useMempool {
		mempoolUrl := os.Getenv("MEMPOOL_API_BASE_URL")
		feeEstimator, err = mempool.NewMempoolClient(mempoolUrl)
		if err != nil {
			log.Fatalf("failed to initialize mempool client: %v", err)
		}

		envFeeStrategy := os.Getenv("MEMPOOL_PRIORITY")
		switch strings.ToLower(envFeeStrategy) {
		case "minimum":
			feeStrategy = chain.FeeStrategyMinimum
		case "economy":
			feeStrategy = chain.FeeStrategyEconomy
		case "hour":
			feeStrategy = chain.FeeStrategyHour
		case "halfhour":
			feeStrategy = chain.FeeStrategyHalfHour
		case "fastest":
			feeStrategy = chain.FeeStrategyFastest
		default:
			feeStrategy = chain.FeeStrategyEconomy
		}
		log.Printf("using mempool api for fee estimation: %v, fee strategy: %v:%v", mempoolUrl, envFeeStrategy, feeStrategy)
	}

	databaseUrl := os.Getenv("DATABASE_URL")
	pool, err := postgresql.PgConnect(databaseUrl)
	if err != nil {
		log.Fatalf("pgConnect() error: %v", err)
	}

	interceptStore := postgresql.NewPostgresInterceptStore(pool)
	forwardingStore := postgresql.NewForwardingEventStore(pool)

	var interceptors []interceptor.HtlcInterceptor
	for _, node := range nodes {
		var htlcInterceptor interceptor.HtlcInterceptor
		if node.Lnd != nil {
			client, err := lnd.NewLndClient(node.Lnd)
			if err != nil {
				log.Fatalf("failed to initialize LND client: %v", err)
			}

			fwsync := lnd.NewForwardingHistorySync(client, interceptStore, forwardingStore)
			interceptor := interceptor.NewInterceptor(client, node, interceptStore, feeEstimator, feeStrategy)
			htlcInterceptor, err = lnd.NewLndHtlcInterceptor(node, client, fwsync, interceptor)
			if err != nil {
				log.Fatalf("failed to initialize LND interceptor: %v", err)
			}
		}

		if node.Cln != nil {
			client, err := cln.NewClnClient(node.Cln.SocketPath)
			if err != nil {
				log.Fatalf("failed to initialize CLN client: %v", err)
			}

			interceptor := interceptor.NewInterceptor(client, node, interceptStore, feeEstimator, feeStrategy)
			htlcInterceptor, err = cln.NewClnHtlcInterceptor(node, client, interceptor)
			if err != nil {
				log.Fatalf("failed to initialize CLN interceptor: %v", err)
			}
		}

		if htlcInterceptor == nil {
			log.Fatalf("node has to be either cln or lnd")
		}

		interceptors = append(interceptors, htlcInterceptor)
	}

	address := os.Getenv("LISTEN_ADDRESS")
	certMagicDomain := os.Getenv("CERTMAGIC_DOMAIN")
	s, err := NewGrpcServer(nodes, address, certMagicDomain, interceptStore)
	if err != nil {
		log.Fatalf("failed to initialize grpc server: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(len(interceptors) + 1)

	stopInterceptors := func() {
		for _, interceptor := range interceptors {
			interceptor.Stop()
		}
	}

	for _, interceptor := range interceptors {
		i := interceptor
		go func() {
			err := i.Start()
			if err == nil {
				log.Printf("Interceptor stopped.")
			} else {
				log.Printf("FATAL. Interceptor stopped with error: %v", err)
			}

			wg.Done()

			// If any interceptor stops, stop everything, so we're able to restart using systemd.
			s.Stop()
			stopInterceptors()
		}()
	}

	go func() {
		err := s.Start()
		if err == nil {
			log.Printf("GRPC server stopped.")
		} else {
			log.Printf("FATAL. GRPC server stopped with error: %v", err)
		}

		wg.Done()

		// If the server stops, stop everything else, so we're able to restart using systemd.
		stopInterceptors()
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-c
		log.Printf("Received stop signal %v. Stopping.", sig)

		// Stop everything gracefully on stop signal
		s.Stop()
		stopInterceptors()
	}()

	wg.Wait()
	log.Printf("lspd exited")
}
