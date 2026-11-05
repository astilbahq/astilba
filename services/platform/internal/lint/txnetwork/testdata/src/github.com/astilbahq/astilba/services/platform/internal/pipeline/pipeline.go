package pipeline

type Provider struct{}

func (*Provider) Resolve(string) (string, error) { return "", nil }
