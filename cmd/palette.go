package cmd

// 颜色序列 —— 每次 useColor() 变化(尤其是 --no-color flag)由
// refreshColorState() 重算;const 不行,因为要响应 PersistentPreRun 后
// 才被 cobra 解析的 flag。代码常量本身(\033[<code>m)是定死的。
const (
	colorReset   = "0"
	colorGreen   = "1;32"
	colorRed     = "0;31"
	colorBRed    = "1;31"
	colorCyan    = "0;36"
	colorBlue    = "1;34"
	colorYellow  = "0;33"
	colorMagenta = "0;35"
	colorDim     = "2"
	colorWhite   = "0;39"
)

// ANSI 转义序列变量 —— useColor() 为 false 时为空串,避免污染管道/CI 日志。
// 由 refreshColorState() 在 rootCmd PersistentPreRun 时按当前 env/flag/TTY
// 状态刷新一次(测试代码也可直接赋值强制开启/关闭)。
var (
	ansiReset   = "\033[0m"
	ansiGreen   = "\033[1;32m"
	ansiRed     = "\033[0;31m"
	ansiBRed    = "\033[1;31m"
	ansiCyan    = "\033[0;36m"
	ansiBlue    = "\033[1;34m"
	ansiYellow  = "\033[0;33m"
	ansiMagenta = "\033[0;35m"
	ansiDim     = "\033[2m"
	// 辅助信息用默认前景色(终端主题的正常文字色),保证可读性;
	// dim(灰)在浅色/低对比主题下难以辨认,仅保留给极不重要的细节。
	ansiWhite = "\033[0;39m"
)

// refreshColorState 按 useColor() 重新计算所有 ansiXxx 变量。RootCmd
// PersistentPreRun 调用一次 — 此时 cobra 已解析完 argv, --no-color flag
// 已确定;env 变量自进程启动不变,只算一次足够。
func refreshColorState() {
	if useColor() {
		ansiReset = "\033[" + colorReset + "m"
		ansiGreen = "\033[" + colorGreen + "m"
		ansiRed = "\033[" + colorRed + "m"
		ansiBRed = "\033[" + colorBRed + "m"
		ansiCyan = "\033[" + colorCyan + "m"
		ansiBlue = "\033[" + colorBlue + "m"
		ansiYellow = "\033[" + colorYellow + "m"
		ansiMagenta = "\033[" + colorMagenta + "m"
		ansiDim = "\033[" + colorDim + "m"
		ansiWhite = "\033[" + colorWhite + "m"
	} else {
		ansiReset = ""
		ansiGreen = ""
		ansiRed = ""
		ansiBRed = ""
		ansiCyan = ""
		ansiBlue = ""
		ansiYellow = ""
		ansiMagenta = ""
		ansiDim = ""
		ansiWhite = ""
	}
}
