#!/bin/sh
# 默认测试入口的薄封装：vet + 全量 go test。
# 集成测试靠环境变量门控自动 skip，需要真实设备/样本的用例按 README「测试」
# 一节显式提供环境变量运行；RACE=1 时为 go test 追加 -race。
set -eu
cd "$(dirname "$0")/.."

# 防宿主 shell 残留的集成测试环境变量误触网络型用例：默认套件必须
# 离线、可重复。变量清单以各 *_test.go 中 os.Getenv 的门控为准。
unset ANYDLNA_PROBE_SAMPLE \
	ANYDLNA_TV_HOST \
	ANYDLNA_STREAM_ITEST \
	ANYDLNA_STREAM_SAMPLE_DIR \
	ANYDLNA_BROWSER_ITEST \
	ANYDLNA_FAKETV_DISCOVER

# 竞态检测开关：RACE=1 ./scripts/test.sh
if [ "${RACE:-0}" = "1" ]; then
	set -- ./... -race
else
	set -- ./...
fi

echo "== go vet =="
go vet ./...

echo "== go test（集成用例无对应环境变量时自动跳过）=="
exec go test "$@"
