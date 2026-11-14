# 天吴 · 常用命令（Windows 上可用 git-bash / WSL / make，或直接用 scripts/ 里的脚本）
#
#   make harness   从仓库外的 harness checkout 构建执行器 -> bin/aiscan-full
#                  （源码不在本仓库：HARNESS_DIR，默认 .build/harness-src）
#   make web       构建控制台前端 -> internal/web/static（产物入库，跑控制面的人不需要 Node）
#   make run       从源码启动控制台（需先 make harness）
#   make test      跑全部测试
#   make race      数据竞争检测（需 cgo/C 工具链；Windows 本机无 gcc 时用 WSL/CI）
#   make fmt       格式化

BIN     ?= bin
EXE     := $(if $(filter windows,$(shell go env GOOS)),.exe,)
TAGS    := forceposix emptytemplates noembed osusergo netgo full sqlite

# 本仓库不内置 harness 源码；把它单独 checkout 到这里或任意目录，再用
# `make harness HARNESS_DIR=<路径>` 指过来。
HARNESS_DIR ?= .build/harness-src

.PHONY: harness web run test race fmt clean

harness:
	@test -f "$(HARNESS_DIR)/go.mod" || { \
	  echo "找不到 $(HARNESS_DIR)/go.mod：本仓库不内置 harness 源码。" >&2; \
	  echo "先单独 clone kksty/cyber-harness 的 tianwu/fixes，再用 HARNESS_DIR 指过去，例如：" >&2; \
	  echo "  git clone --branch tianwu/fixes https://github.com/kksty/cyber-harness.git .build/harness-src" >&2; \
	  echo "  make harness HARNESS_DIR=.build/harness-src" >&2; \
	  exit 1; \
	}
	cd "$(HARNESS_DIR)" && go build -tags "$(TAGS)" -o "$(CURDIR)/$(BIN)/aiscan-full$(EXE)" ./cmd/aiscan
	@echo "ok: $(BIN)/aiscan-full$(EXE)"

# 前端产物是 go:embed 的来源，所以它入库；只有改界面时才需要 Node。
web:
	pnpm install
	pnpm build
	@echo "ok: internal/web/static"

run:
	go run ./cmd/tw --addr 127.0.0.1:8080 --data ./data --aiscan ./$(BIN)/aiscan-full$(EXE)

test:
	go test ./...

# 数据竞争检测：-race 需要 cgo/C 工具链（Linux gcc / Windows MinGW-w64 gcc）。
# 缺编译器时给出可读指引，而不是一个难懂的 cgo 错误；CI 跑同一组包。
RACE_PKGS ?= ./internal/api ./internal/lifecycle ./internal/verifier ./internal/store ./internal/decider ./internal/board ./internal/collab

race:
	@command -v "$$(go env CC)" >/dev/null 2>&1 || { \
	  echo "race 检测需要 C 工具链（cgo）：本机没有 $$(go env CC)。" >&2; \
	  echo "请用 WSL/Docker/Linux 跑 make race，或看 CI：.github/workflows/race.yml" >&2; \
	  exit 1; \
	}
	CGO_ENABLED=1 go test -race -count=1 -timeout 30m $(RACE_PKGS)

fmt:
	gofmt -w cmd internal

clean:
	rm -rf $(BIN)
