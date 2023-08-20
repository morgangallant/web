package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type job struct {
	name string
	when string
	work workFunc
}

type workFunc func(logger *zap.Logger) error

var jobs = []job{
	{
		name: "gallant.com out-of-biz check",
		when: "@every 1d",
		work: urlResponsivenessCheck(
			"https://gallant.com",
			func(logger *zap.Logger, code int, status string) error {
				return notify(
					context.Background(),
					logger,
					"gallant.com may be out of business! Daily ping returned status %d (%s).",
					code,
					status,
				)
			},
		),
	},
}

func urlResponsivenessCheck(url string, cb func(*zap.Logger, int, string) error) workFunc {
	return func(logger *zap.Logger) error {
		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("failed to GET %s: %w", url, err)
		} else if resp.StatusCode >= 400 {
			if err := cb(logger, resp.StatusCode, resp.Status); err != nil {
				return err
			}
		}
		return nil
	}
}

func notify(ctx context.Context, logger *zap.Logger, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	// todo: send telegram message
	return nil
}

func jobsCronServer(logger *zap.Logger) (*cron.Cron, int) {
	server := cron.New()
	for _, job := range jobs {
		server.AddFunc(job.when, func() {
			fields := []zap.Field{
				zap.String("name", job.name),
				zap.String("when", job.when),
			}
			logger.Info("starting job", fields...)
			if err := job.work(logger); err != nil {
				fields = append(fields, zap.Error(err))
				logger.Error(
					"job failed",
					fields...,
				)
				return
			}
			logger.Info(
				"job completed successfully",
				fields...,
			)
		})
	}
	return server, len(jobs)
}
