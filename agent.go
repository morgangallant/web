package main

import (
	"errors"
	"fmt"
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

func (a *agent) ownerChatID() (*int64, error) {
	key := fmt.Sprintf("telegram:user:%s:chat_id", owner)
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
