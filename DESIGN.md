# 设计说明:站点登录状态(Cookies)的两种方式

设置页「读取本机浏览器」与「用应用登录浏览器」两种方式取舍的技术背景：

关于两种登录方式的取舍，有几点基于实测的说明：

- **「读取本机浏览器」不做数据复制**，由 yt-dlp 直接读取浏览器自身的 Cookie 存储并解密，因此始终与你浏览器里的登录状态一致。首次在 macOS 上读取 Chrome 时系统会弹出钥匙串授权，请点「允许」。
- **「用应用登录浏览器」必须使用独立 profile（技术限制，非实现选择）**：Chrome 136 起，`--remote-debugging-port` 在默认数据目录下会被忽略，官方推荐自动化场景使用独立数据目录（见 [Chrome 官方说明](https://developer.chrome.com/blog/remote-debugging-port)）；此外同一 profile 不能被两个进程同时打开，共享 profile 将要求你先完全退出日常浏览器。因此这里刻意不共用日常 profile。
- **复制日常 profile 的做法不可行**：Chrome 的 App-Bound Encryption 把 Cookie 密钥绑定在原 profile 上，复制到别处后登录态无法解密（实测复制后仅能读到匿名 Cookie）。这也是「读取本机浏览器」由 yt-dlp 原地读取、而非应用先复制的原因。
