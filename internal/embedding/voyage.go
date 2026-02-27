package embedding

type VoyageClient struct {
	apiKey     string
	model      string
	dimensions int
}

func NewVoyageClient(apiKey, model string, dimensions int) *VoyageClient {
	return &VoyageClient{apiKey: apiKey, model: model, dimensions: dimensions}
}
