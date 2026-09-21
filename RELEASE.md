# 发布流程（RELEASE）

本文档描述 AnyDLNA macOS 版的**手动发布流程**：版本号约定 → 构建 → Developer ID 签名 → 公证 → 校验。目前发布全在本机手动完成，CI（`.github/workflows/ci.yml`）只负责测试与交叉编译校验，不做构建产物与分发。

> 本机自用直接跑 `./scripts/build-app.sh` 即可（adhoc 签名，拷给他人会被 Gatekeeper 拦截）；本文流程只在**对外分发**时需要，且需要 Apple Developer 账号。发布机需为 arm64 macOS（`bundle-tools.sh` 中的 ffmpeg 静态包仅提供 arm64 构建）。

## 版本号：wails.json 为单一来源

- 版本号的唯一来源是 `wails.json` 的 `info.productVersion`（构建时由 wails 写入 `Info.plist` 的 `CFBundleShortVersionString`），不要在其他文件里另维护一份版本号。
- 发版流程：
  1. 把 `productVersion` 更新为目标版本（语义化版本，如 `1.2.0`）并提交；
  2. 代码合并进 main 后，在**同一 commit** 上打 tag：`git tag v1.2.0`；
  3. tag 的 `X.Y.Z` 必须与 `productVersion` 完全一致——两者不一致时，以 `productVersion` 为准修正 tag 或版本号，再进入构建。
- tag 之后 `productVersion` 又有新提交的，属于未发布内容，不打 tag、不发布。

## 发布步骤

### 前置条件（一次性）

- Xcode / Command Line Tools（`codesign`、`notarytool`、`stapler` 随附）。
- Keychain 中有 **Developer ID Application** 证书（Apple Developer 后台申请）：

  ```bash
  security find-identity -v -p codesigning
  # 形如：1) XXXXXXXX... "Developer ID Application: <名字> (<TeamID>)"
  ```

- 登记公证凭据到钥匙串（只需一次，后续公证均引用该 profile）：

  ```bash
  xcrun notarytool store-credentials AnyDLNA --apple-id <AppleID邮箱> --team-id <TeamID>
  # 交互式输入 App 专用密码（appleid.apple.com 生成）；
  # 也可改用 App Store Connect API key（--key/--key-id/--issuer）。
  ```

### 1. 构建

```bash
wails build                                          # 产出 build/bin/AnyDLNA.app
./scripts/bundle-tools.sh build/bin/AnyDLNA.app      # 塞入 pin 好版本的 yt-dlp / ffmpeg / qjs
```

（`./scripts/build-app.sh` 把以上两步与 adhoc 重签封装在一起；发布版不用它的签名步骤，手动走下文。）

### 2. Developer ID 签名

先签塞进 `Resources/tools` 的三个外部二进制（裸 Mach-O 不是嵌套 bundle，`--deep` 不会替你签好），再整体签 `.app`：

```bash
APP="build/bin/AnyDLNA.app"
IDENTITY="Developer ID Application: <名字> (<TeamID>)"

for bin in "$APP"/Contents/Resources/tools/*; do
  codesign --force --sign "$IDENTITY" --options runtime "$bin"
done
codesign --force --sign "$IDENTITY" --options runtime "$APP"
codesign --verify --deep --strict "$APP"
```

`--options runtime` 启用 Hardened Runtime，是公证的前置条件。若某个自带工具在 Hardened Runtime 下运行异常，需为它单独补相应 entitlement（如 `com.apple.security.cs.allow-unsigned-executable-memory`）后重签该二进制与 `.app`。

### 3. 公证（notarytool）

```bash
ditto -c -k --keepParent "$APP" AnyDLNA.zip          # 公证用 zip 必须保留 .app 目录结构
xcrun notarytool submit AnyDLNA.zip --keychain-profile AnyDLNA --wait
```

- `--wait` 会阻塞到公证结束（通常几分钟）；被拒时查看具体原因：

  ```bash
  xcrun notarytool log <submission-id> --keychain-profile AnyDLNA
  # submission-id 取自 submit 的输出；日志 JSON 的 statusDescription 列出每条问题。
  ```

- 修复问题后重新签名并提交即可，公证可无限次重试。

### 4. 装订（staple）与最终校验

```bash
xcrun stapler staple "$APP"                          # 把公证票据装订进 .app，用户离线也能过 Gatekeeper
xcrun stapler validate "$APP"
spctl -a -vv "$APP"                                  # 应显示 accepted，来源为 Developer ID
```

装订后的 `.app` 不可再改动（改动即失效，需重新签名公证）。最后用 `ditto -c -k --keepParent` 打分发 zip。

## 自带工具与 sha256 pin

`scripts/bundle-tools.sh` 把外部工具以固定版本打进 `.app`（用户侧零安装、行为确定）：

- 每个下载物都经 sha256 pin 校验（供应链防护）：`YTDLP_SHA256` / `QUICKJS_SHA256` / `FFMPEG_SHA256`，与 `YTDLP_VERSION` / `QUICKJS_VERSION` 一一对应。
- 版本、URL、哈希均可用同名环境变量覆盖（如 `FFMPEG_URL`），便于换源重打；**覆盖版本或 URL 时必须同步覆盖对应哈希**，否则校验失败即中止（不产出半成品包）。
- 升级某工具 = 改脚本内版本号 + 换上官方新分发物核对后的哈希，一起提交。ffmpeg 的 zip 无版本号（URL 恒定、内容随官方更新），哈希 pin 的是当前快照，校验失败即说明官方换包，需重新核对。
- 发布前建议跑一遍全新缓存（`CACHE_DIR=/tmp/anydlna-cache ./scripts/bundle-tools.sh build/bin/AnyDLNA.app`），确认 pin 的 URL 与哈希仍然有效。
