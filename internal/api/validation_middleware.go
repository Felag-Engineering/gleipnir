package api

import "github.com/rapp992/gleipnir/internal/httputil"

// MaxRequestBodySize is the default body size limit (1 MiB) applied to all
// API and trigger endpoints. Value is kept in sync with httputil.MaxRequestBodySize.
const MaxRequestBodySize = 1 << 20

// BodySizeLimit returns middleware that caps the request body at maxBytes.
// Delegates to httputil.BodySizeLimit; kept here for backward compatibility.
var BodySizeLimit = httputil.BodySizeLimit

// RequireJSON is middleware that rejects POST/PUT/PATCH requests whose
// Content-Type does not contain "application/json" with 415 Unsupported Media Type.
// Delegates to httputil.RequireJSON; kept here for backward compatibility.
var RequireJSON = httputil.RequireJSON
