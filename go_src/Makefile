#!make
-include ../.env
export

BINARY := rosfin-terrorists

.PHONY: run once test vet build build_for_win clean

## Разовое скачивание из локальной сборки
once:
	go run . -once -output ../downloads

## Запуск демона (проверка в окнах ROSFIN_WINDOWS, по умолчанию 09–10 и 17–18)
run:
	go run . -output ../downloads

test:
	go test ./...

vet:
	go vet ./...

build: vet test
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o build/$(BINARY) .

build_for_win:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o build/$(BINARY).exe .

clean:
	rm -rf build
