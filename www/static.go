package www

import "embed"

//go:embed *.html
//go:embed *.js
//go:embed *.json
//go:embed webos-pip
var Static embed.FS
