package indexer

import (
	"github.com/AustinSchoen/codebase-intel/internal/config"
)

type Indexer struct {
	cfg *config.Config
}

func New(cfg *config.Config) (*Indexer, error) {
	return &Indexer{cfg: cfg}, nil
}

func (i *Indexer) Run() error {
	// TODO: Implement indexing pipeline
	return nil
}
