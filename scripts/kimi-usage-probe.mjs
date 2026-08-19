#!/usr/bin/env node

/**
 * kimi-usage-probe.mjs — 调试 Kimi Coding Plan 真实 API 形态。
 *
 * 用法:
 *   KIMI_CODING_API_KEY=sk-... node scripts/kimi-usage-probe.mjs
 *
 * 注意: token 只能从环境变量读,不要走命令行参数(避免进 shell history)。
 * 与 submon 同序:`KIMI_CODING_API_KEY` 优先,`KIMI_API_KEY` 兜底。
 *
 * 输出:
 *   1. 状态码 + Content-Type
 *   2. 前 ~2KB 的 pretty JSON
 *   3. 类型树(JSONPath + 每节点 typeof / 数组首个样本)
 *   4. 完整响应落盘到 ./kimi-probe-*.json
 *
 * 试两套 base URL(api.kimi.com/coding/v1 与 api.moonshot.cn/v1),
 * 每个 base 都按 /usages → 404 → /usage 的顺序回退。
 */

import { writeFileSync } from "node:fs";

const token = process.env.KIMI_CODING_API_KEY ?? process.env.KIMI_API_KEY;
if (!token) {
	console.error("未设置 KIMI_CODING_API_KEY / KIMI_API_KEY 环境变量");
	console.error("");
	console.error("用法: KIMI_CODING_API_KEY=sk-... node scripts/kimi-usage-probe.mjs");
	process.exit(1);
}

const bases = [
	"https://api.kimi.com/coding/v1",
	"https://api.moonshot.cn/v1",
];

async function probe(base) {
	for (const path of ["/usages", "/usage"]) {
		const url = base + path;
		process.stdout.write(`\n=== GET ${url} ===\n`);
		let resp;
		try {
			resp = await fetch(url, {
				headers: {
					Authorization: "Bearer " + token,
					"User-Agent": "KimiCLI/1.6",
					Accept: "application/json",
				},
			});
		} catch (e) {
			console.error("fetch failed:", e.message);
			continue;
		}
		console.log(`status: ${resp.status} ${resp.statusText}`);
		console.log(`content-type: ${resp.headers.get("content-type") ?? ""}`);
		const body = await resp.text();
		if (!resp.ok) {
			console.log(`body (${body.length} bytes, first 400): ${body.slice(0, 400)}`);
			continue;
		}
		let parsed;
		try {
			parsed = JSON.parse(body);
		} catch {
			console.log(`非 JSON body (${body.length} bytes, first 400): ${body.slice(0, 400)}`);
			continue;
		}

		// 类型树 — 帮定位字段是 string 还是 number(这正是 submon 之前栽过的坑)。
		console.log("--- 类型树 ---");
		walk(parsed);

		// pretty JSON 的前 2KB 概览。
		const pretty = JSON.stringify(parsed, null, 2);
		console.log("--- pretty JSON (前 2KB) ---");
		console.log(pretty.slice(0, 2048));
		if (pretty.length > 2048) console.log(`... (共 ${pretty.length} 字节,落盘见下)`);

		// 完整响应落盘,方便后续拼进 submon 测试 fixture。
		const safe = base.replace(/[^a-z0-9]/gi, "_") + path.replace(/\//g, "_");
		const file = `kimi-probe-${safe}.json`;
		writeFileSync(file, pretty);
		console.log(`--- 完整响应: ${file} ---`);
	}
}

function walk(value, path = "$") {
	if (value === null) {
		console.log(`${path}: null`);
		return;
	}
	const t = typeof value;
	if (t === "string" || t === "number" || t === "boolean") {
		const sample = t === "string" ? JSON.stringify(value.slice(0, 60)) : String(value);
		console.log(`${path}: ${t} = ${sample}`);
		return;
	}
	if (Array.isArray(value)) {
		console.log(`${path}: array[${value.length}]`);
		if (value.length > 0) walk(value[0], `${path}[0]`);
		return;
	}
	console.log(`${path}: object`);
	for (const [k, v] of Object.entries(value)) {
		walk(v, `${path}.${k}`);
	}
}

for (const b of bases) {
	try {
		await probe(b);
	} catch (e) {
		console.error(`probe ${b} failed:`, e.message);
	}
}