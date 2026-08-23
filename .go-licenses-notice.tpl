{{- range . }}{{ printf "%s\t%s\t%s\t%s\t%s\n" .Name .Version .LicenseName .LicenseURL .LicensePath }}{{- end }}
