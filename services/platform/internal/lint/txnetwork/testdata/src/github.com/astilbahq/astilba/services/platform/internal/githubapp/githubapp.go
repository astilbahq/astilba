package githubapp

type Client struct{}

func (*Client) Mint() (string, error) { return "", nil }
func (*Client) FixtureAPI() string    { return "" }
func (*Client) InstallURL() string    { return "" }
func (*Client) AuthorizeURL() string  { return "" }
func (*Client) VerifyWebhook() error  { return nil }
