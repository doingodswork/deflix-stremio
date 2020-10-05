package logadapter

import "go.uber.org/zap"

type Badger2Zap struct {
	*zap.SugaredLogger
}

// NewBadger2Zap creates a new Badger2Zap logger
func NewBadger2Zap(logger *zap.Logger) *Badger2Zap {
	return &Badger2Zap{
		SugaredLogger: logger.Sugar(),
	}
}

func (logger *Badger2Zap) Warningf(template string, args ...interface{}) {
	logger.Warnf(template, args...)
}
