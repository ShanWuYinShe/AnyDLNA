#!/bin/sh
# 默认测试入口的薄封装：vet + 全量 go test。
# 集成测试靠环境变量门控自动 skip（无 env 时空跑），需要真实设备/样本的
# 用例按 README「测试」一节显式提供环境变量运行。
set -eu
cd "$(dirname "$0")/.."

echo "== go vet =="
go vet ./...

echo "== go test（集成用例无对应环境变量时自动跳过）=="
exec go test ./...
