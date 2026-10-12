# moderate の NG ワード削除をまとめる

対象: ISUCON13 練習（ISUPipe / Go 参考実装）  
変更ファイル: `go/livecomment_handler.go` の `moderateHandler`（NG ワード登録後の過去コメント削除のみ）  
目的: `POST /api/livestream/:livestream_id/moderate` で、全配信の `livecomments` を読み出しコメントごとに DELETE していた処理を、**当該配信 × NG ワードごと 1 本の DELETE** にまとめる。

関連: [チャット引き継ぎ](./10-チャット引き継ぎ.md) の 6.2、[ユーザー統計のランク集計をまとめる](./09-ユーザー統計のランク集計をまとめる.md)

---

## 背景

- 最適化前 journal で `POST .../moderate` が約 15.76 秒、`failed to delete old livecomments that hit spams`。
- 旧実装は NG ワードごとに `SELECT * FROM livecomments`（**全配信**）し、各行で部分一致判定付き `DELETE` を 1 本ずつ実行していた。

---

## 対象 API

| HTTP | ハンドラ |
|------|----------|
| `POST /api/livestream/:livestream_id/moderate` | `moderateHandler` |

変更しない箇所: 配信者検証、NG ワード INSERT、`ng_words` の取得、レスポンスの `word_id`。`postLivecommentHandler` のスパム判定 SQL もそのまま。

---

## 旧実装

1. `SELECT * FROM ng_words WHERE livestream_id = ?` で当該配信の NG ワード一覧
2. 各 NG ワードについて:
   - `SELECT * FROM livecomments`（テーブル全件）
   - 各コメントについて、次の条件で `DELETE`（`livestream_id` はパスの配信 ID）

```sql
DELETE FROM livecomments
WHERE id = ? AND livestream_id = ? AND
  (SELECT COUNT(*) FROM
    (SELECT ? AS text) AS texts
    INNER JOIN (SELECT CONCAT('%', ?, '%') AS pattern) AS patterns
    ON texts.text LIKE patterns.pattern) >= 1
```

部分一致は `comment LIKE CONCAT('%', ngword, '%')` と同じ意味（投稿時のスパム判定と同型）。

---

## 新実装

当該配信に限定し、NG ワードごとに 1 回だけ削除する。

```sql
DELETE FROM livecomments
WHERE livestream_id = ? AND comment LIKE CONCAT('%', ?, '%')
```

登録直後は `ng_words` に新語も含まれるため、既存ワード分の DELETE は実質 no-op（該当行なし）になり、新語にだけヒットする行が消える。旧実装と同じく「その配信の全 NG ワードで過去コメントを掃除する」挙動。

---

## ベンチ（before）

6.1 反映後（`log_nottrack/bench4.txt`）。

| スコア | 視聴完了 |
|--------|----------|
| 4801 | 20 |

本番反映後は同じコマンドで再計測する。

```sh
cd ~/webapp/go
go build -o isupipe .
sudo systemctl restart isupipe-go.service
cd ~
./bench run --target http://pipe.u.isucon.local:8080
```

確認: スコア、視聴完了、moderate 系シナリオ失敗、journal の `failed to delete old livecomments that hit spams` と moderate の latency。

---

## 次の候補（同時に直さない）

- 6.3 `getLivestreamStatisticsHandler` の配信ごとランク 2 クエリ
- 6.5 `fillUserResponse` / icon
