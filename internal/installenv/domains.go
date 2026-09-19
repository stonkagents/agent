// Package installenv also knows the product's public domains, so every
// allowlist that names the portal hosts derives from one list.
package installenv

// ProductDomains are the registrable domains the portal is served from.
// The first is the primary (new) domain; every later one stays live for
// installed clients that bake it (tracker.*.stonkagents.com, releases.*).
var ProductDomains = []string{"stonkagents.com"}

// PortalOrigins are the deployed portal origins on every product domain:
// https://dev.<d>, https://stg.<d> and https://<d>. Shared by the daemon and
// the controller CORS allowlists; CORS_ALLOWED_ORIGINS adds to them.
func PortalOrigins() []string {
	out := make([]string, 0, 3*len(ProductDomains))
	for _, d := range ProductDomains {
		out = append(out, "https://dev."+d, "https://stg."+d, "https://"+d)
	}
	return out
}
