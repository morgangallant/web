package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/syndtr/goleveldb/leveldb"
	"go.uber.org/zap"
)

// Yes, the website has an agent
// I'm sorry...

const owner = "MorganGallant"

type agent struct {
	logger *zap.Logger
	db     *leveldb.DB
}

func (a *agent) telegramChatId(user string) (*int64, error) {
	key := fmt.Sprintf("telegram:user:%s:chat_id", user)
	value, err := a.db.Get([]byte(key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	parsed, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (a *agent) setUserTelegramChatId(user string, chatId int64) error {
	key := fmt.Sprintf("telegram:user:%s:chat_id", user)
	if err := a.db.Put(
		[]byte(key),
		[]byte(strconv.FormatInt(chatId, 10)),
		nil,
	); err != nil {
		return err
	}
	return nil
}

func (a *agent) sendTelegramMessage(ctx context.Context, chatId int64, msg string) error {
	tkey, ok := os.LookupEnv("TELEGRAM_KEY")
	if !ok {
		return errors.New("missing TELEGRAM_KEY environment variable")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tkey)
	body, err := json.Marshal(map[string]any{
		"chat_id": chatId,
		"text":    msg,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	} else if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to send telegram message: %s", resp.Status)
	}
	return nil
}

type telegramMessage struct {
	ID      int64 `json:"update_id"`
	Message struct {
		ID   int64  `json:"message_id"`
		Text string `json:"text"`
		Chat struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"chat"`
		User *struct {
			ID        int64   `json:"id"`
			FirstName string  `json:"first_name"`
			Username  *string `json:"username"`
		} `json:"from"`
	} `json:"message"`
}

func (a *agent) handleIncomingTelegram(ctx context.Context, msg *telegramMessage) error {
	if msg.Message.User == nil || msg.Message.User.Username == nil {
		return nil
	}
	username := *msg.Message.User.Username

	chatId, err := a.telegramChatId(username)
	if err != nil {
		return err
	} else if chatId == nil || *chatId != msg.Message.Chat.ID {
		if err := a.setUserTelegramChatId(username, msg.Message.Chat.ID); err != nil {
			return err
		}
	}

	if username != owner {
		if err := a.sendTelegramMessage(
			ctx,
			msg.Message.Chat.ID,
			"Sorry, I only respond to my owner right now.",
		); err != nil {
			return err
		}
		return nil
	}

	return a.sendTelegramMessage(
		ctx,
		msg.Message.Chat.ID,
		"Hello owner!",
	)
}
