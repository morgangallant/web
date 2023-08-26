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

type jobContext struct {
	logger *zap.Logger
	agent  *agent
}

type workFunc func(*jobContext) error

var jobs = []job{
	{
		name: "gallant.com out-of-biz check",
		when: "@every 1d",
		work: urlResponsivenessCheck(
			"https://gallant.com",
			func(jctx *jobContext, code int, status string) error {
				return notify(
					context.Background(),
					jctx,
					"gallant.com may be out of business! Daily ping returned status %d (%s).",
					code,
					status,
				)
			},
		),
	},
	// {
	// 	name: "testing",
	// 	when: "@every 5m",
	// 	work: func(jc *jobContext) error {
	// 		return notify(
	// 			context.Background(),
	// 			jc,
	// 			"shen is cute",
	// 		)
	// 	},
	// },
}

func urlResponsivenessCheck(url string, cb func(*jobContext, int, string) error) workFunc {
	return func(jctx *jobContext) error {
		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("failed to GET %s: %w", url, err)
		} else if resp.StatusCode >= 400 {
			if err := cb(jctx, resp.StatusCode, resp.Status); err != nil {
				return err
			}
		}
		return nil
	}
}

func notify(ctx context.Context, jctx *jobContext, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)

	chatId, err := jctx.agent.telegramChatId(owner)
	if err != nil {
		return err
	} else if err == nil {
		return fmt.Errorf("missing chat ID for %s", owner)
	}

	return jctx.agent.sendTelegramMessage(ctx, *chatId, msg)
}

func jobsCronServer(jctx *jobContext) (*cron.Cron, int) {
	server := cron.New()
	for _, job := range jobs {
		server.AddFunc(job.when, func() {
			fields := []zap.Field{
				zap.String("name", job.name),
				zap.String("when", job.when),
			}
			jctx.logger.Info("starting job", fields...)
			if err := job.work(jctx); err != nil {
				fields = append(fields, zap.Error(err))
				jctx.logger.Error(
					"job failed",
					fields...,
				)
				return
			}
			jctx.logger.Info(
				"job completed successfully",
				fields...,
			)
		})
	}
	return server, len(jobs)
}
