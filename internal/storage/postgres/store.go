package postgres

type Store struct {
	host     string
	port     int
	db       string
	maxConns int
}

func NewStore(host string, port int, db string, maxConns int) *Store {
	return &Store{host: host, port: port, db: db, maxConns: maxConns}
}
