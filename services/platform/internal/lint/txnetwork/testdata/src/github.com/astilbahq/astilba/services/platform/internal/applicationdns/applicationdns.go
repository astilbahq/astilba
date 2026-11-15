package applicationdns

type Cloudflare struct{ Zone string }

func (*Cloudflare) Reconcile(name string) error { return nil }
func (*Cloudflare) Remove(name string) error    { return nil }
