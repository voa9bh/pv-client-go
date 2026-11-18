GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o pvbrowser.exe ./cmd/pvbrowser
go build -o pvbrowser ./cmd/pvbrowser