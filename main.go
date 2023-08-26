package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/joho/godotenv"
	"github.com/syndtr/goleveldb/leveldb"
	"go.uber.org/zap"
	"golang.org/x/exp/constraints"
)

var requiredVars = []string{
	"TELEGRAM_KEY",
}

func main() {
	var logger *zap.Logger
	if v, ok := os.LookupEnv("RAILWAY_ENVIRONMENT"); ok && v == "production" {
		logger, _ = zap.NewProduction()
	} else {
		logger, _ = zap.NewDevelopment()
	}
	defer logger.Sync()

	if runtime.GOOS == "darwin" {
		const envf = ".env"
		logger.Info(
			"checking for local development environment variables",
			zap.String("goos", runtime.GOOS),
			zap.String("file", envf),
		)
		_ = godotenv.Load(envf)
	}

	for _, reqv := range requiredVars {
		if _, ok := os.LookupEnv(reqv); !ok {
			logger.Fatal("missing required environment variable", zap.String("variable", reqv))
		}
	}

	rctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(rctx, cancel, logger); err != nil {
		logger.Fatal("top-level error", zap.Error(err))
	}
}

func run(ctx context.Context, shutdown context.CancelFunc, logger *zap.Logger) error {
	spath := "data"
	if mp, ok := os.LookupEnv("RAILWAY_VOLUME_MOUNT_PATH"); ok {
		spath = mp
	}

	db, err := leveldb.OpenFile(spath, nil)
	if err != nil {
		return err
	}
	defer db.Close()

	ag := &agent{
		logger: logger,
		db:     db,
	}

	tset, err := newTemplateSet("embed/templates", embedded)
	if err != nil {
		return fmt.Errorf("failed to load templates: %w", err)
	}

	const blogPrefix = "/blog/"

	blogPostHandler, posts, err := blogHandler(logger, tset, blogPrefix)
	if err != nil {
		return err
	}

	staticHandler, err := staticHandler()
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthCheckHandler())
	mux.Handle("/webhook/telegram", telegramHandler(ag))
	mux.HandleFunc(blogPrefix, blogPostHandler)
	mux.HandleFunc("/feed.xml", rssFeedHandler(posts))
	mux.HandleFunc("/", tset.handlerWithFallback(
		func(r *http.Request) (any, error) {
			return struct {
				commonTemplateData
				RecentPosts []BlogPost
			}{
				commonTemplateData: loadCommonTemplateData(r),
				RecentPosts:        posts[:min(len(posts), 3)],
			}, nil
		},
		staticHandler,
	))

	port, ok := os.LookupEnv("PORT")
	if !ok {
		logger.Info("no port specified, defaulting to :8080")
		port = "8080"
	}

	srv := &http.Server{
		Addr: fmt.Sprintf("0.0.0.0:%s", port),
		Handler: withMiddleware(
			mux,
			initiatedAtMiddleware, // Records when we first got request
		),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second * 10,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server errored, shutting down", zap.Error(err))
			shutdown()
		}
	}()

	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
		defer cancel()

		if err := srv.Shutdown(sctx); err != nil {
			logger.Warn("http server shutdown errored", zap.Error(err))
		}
	}()

	logger.Info("started http server", zap.String("port", port))

	jc := &jobContext{
		logger: logger,
		agent:  ag,
	}

	crons, njobs := jobsCronServer(jc)
	crons.Start()
	defer crons.Stop()

	logger.Info("started cron server", zap.Int("jobs", njobs))

	<-ctx.Done()

	return nil
}

func healthCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("OK")); err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func telegramHandler(agent *agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(telegramMessage)
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := agent.handleIncomingTelegram(r.Context(), req); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

func withMiddleware(
	base http.Handler,
	mfs ...func(http.Handler) http.Handler,
) http.Handler {
	for i := len(mfs) - 1; i >= 0; i-- {
		base = mfs[i](base)
	}
	return base
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}
