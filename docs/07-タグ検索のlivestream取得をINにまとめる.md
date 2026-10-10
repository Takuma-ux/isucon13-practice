# タグ検索の livestream 取得を IN にまとめる

対象: ISUCON13 練習（ISUPipe / Go 参考実装）  
変更ファイル: `go/livestream_handler.go` の `searchLivestreamsHandler` のみ  
目的: タグ検索で、配信本体を 1 件ずつ取っていた SQL を 1 回の `IN` にする。

関連: [次の過去問への取り組み方と Cursor 指示](./06-次の過去問への取り組み方とCursor指示.md)

この文書は、以前の「一覧 fill までまとめた N+1 削減」から、レビューできる最初の 1 変更だけに狭めたもの。

---

## 背景

- ベンチでは `livestream/search` が頻出する。
- タグ指定時、`livestream_tags` の行ごとに `SELECT * FROM livestreams WHERE id = ?` していた。
- スコアは Tip 合計（`manual.md`）。このコミットは、その前段のタグ検索の往復回数だけを減らす。

一覧を返すあとの owner・タグ組み立て（`fillLivestreamResponse`）は、このコミットでは変えない。

---

## 対象 API

| HTTP | ハンドラ | 変更 |
|------|----------|------|
| `GET /api/livestream/search?tag=...` | `searchLivestreamsHandler` | タグに紐づく livestream の取得を `IN` 1 回にする |

`tag` なしの検索、`GET /api/livestream`、`GET /api/user/:username/livestream`、`GET /api/livestream/:id` は変更していない。

---

## 変更内容

**Before:** `livestream_tags` の各行ごとに `SELECT * FROM livestreams WHERE id = ?`

**After:** ID を集めて 1 回で取る。該当が 0 件のときはこのクエリを出さない。

```sql
SELECT * FROM livestreams WHERE id IN (?) ORDER BY id DESC
```

取得したあとは、従来どおり 1 件ずつ `fillLivestreamResponse` する。

---

## このコミットに含めていないもの

| 内容 | 理由 |
|------|------|
| 一覧の `fillLivestreamsResponse` | 次のコミット。owner・theme・icon・タグの N+1 |
| 単体取得のタグ JOIN | 次のコミット |
| `user_handler.go` / `livecomment_handler.go` / `stats_handler.go` | チーム分担 |
| `reaction_handler.go` | 別コミット |
| INDEX、`main.go` の DB プール | 別コミット |

---

## デプロイ

```sh
cd ~/webapp/go
go build -o isupipe .
sudo systemctl restart isupipe-go.service
sudo systemctl is-active isupipe-go.service
```

---

## 検証

変更前のスコアをメモしてから、同じコマンドでベンチする。

```sh
./bench run --target http://pipe.u.isucon.local:8080
```

記録する項目:

- スコア（Tip 合計）
- 配信を最後まで視聴できた視聴者数
- シナリオの成功と失敗
- 最終チェックの成功と失敗

---

## チーム報告

```markdown
### 変更
- ファイル: `go/livestream_handler.go`
- 内容: タグ検索の livestream 取得を `IN` 1 回にした。fill は未変更
- コミット: `<hash>`

### ベンチ（同一 target）
| | スコア | 視聴完了 | 備考 |
|--|--------|----------|------|
| before | | | |
| after | | | |

### 判断
- pass（最終チェック）:
- スコア差の要因:
```

---

## 次のコミット候補

| 優先 | 内容 | ファイル |
|------|------|----------|
| 次 | 一覧 fill の一括化（owner / theme / icon / tags） | `livestream_handler.go` |
| ② | `GET .../reaction` の fill 重複削減 | `reaction_handler.go` |
| ② | livecomment の N+1 | `livecomment_handler.go`（担当） |
| ② | INDEX | `10_schema.sql`（担当者と調整） |
| ③ | `SetMaxOpenConns` 等 | `main.go` |
