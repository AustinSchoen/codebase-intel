package qdrant

type Client struct {
	url              string
	collectionPrefix string
}

func NewClient(url, collectionPrefix string) *Client {
	return &Client{url: url, collectionPrefix: collectionPrefix}
}
