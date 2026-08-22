weavegate
Copyright 2026 weavegate contributors

This product includes the following third-party runtime dependencies. Regenerate
this inventory from the repository root with:

go list std | sed 's#^#--ignore=#' | xargs go run github.com/google/go-licenses@v1.6.0 report --template .go-licenses-notice.tpl --ignore=github.com/weavegate/weavegate ./cmd/weavegate > NOTICE

Go standard library packages and the weavegate module itself are excluded.

Package,License,License source
{{- range . }}
{{ .Name }},{{ .LicenseName }},{{ .LicenseURL }}
{{- end }}
