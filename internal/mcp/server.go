package mcp

import (
	"github.com/AustinSchoen/codebase-intel/internal/config"
)

type Server struct {
	cfg *config.Config
}

func NewServer(cfg *config.Config) *Server {
	return &Server{cfg: cfg}
}

func (s *Server) Run() error {
	// TODO: Implement MCP stdio transport
	return nil
}
