# 一覧 fill を IN にまとめる

対象: ISUCON13 練習（ISUPipe / Go 参考実装）  
変更ファイル: `go/livestream_handler.go`  
目的: 配信の owner・theme・icon・タグを、件数ぶんの SQL から固定本数の `IN` にする。一覧も1件取得も同じ関数を使う。

関連: [タグ検索の livestream 取得を IN にまとめる](./07-タグ検索のlivestream取得をINにまとめる.md)

この差分はこれ以上コミットを分けない。owner 側とタグ側は同じ `IN` + map で、呼び出し側もこの関数と一緒に変える必要がある。途中で片側だけ残すと、レビュー用の仮実装がもう1本増える。

---

## 背景

- タグ検索の配信本体は、前のコミットで `IN` 1 回になっている。
- その後の `fillLivestreamResponse` が、配信1件ごとに users / themes / icons / livestream_tags / tags を取っていた。
- 前のベンチ（スコア 2546、視聴完了 6）の落ちは、停止時の `context canceled` と DNS タイムアウトであり、この fill はまだ本番に載っていない。

---

## 対象 API

| HTTP | ハンドラ |
|------|----------|
| `GET /api/livestream/search` | `searchLivestreamsHandler` |
| `GET /api/livestream` | `getMyLivestreamsHandler` |
| `GET /api/user/:username/livestream` | `getUserLivestreamsHandler` |

3つとも、ループ内の `fillLivestreamResponse` を `fillLivestreamsResponse` に替えている。

`fillLivestreamResponse` は削除した。1件取得も `fillLivestreamsResponse` に1件のスライスを渡す。仲間が触る呼び出しは次の4つ。

| 呼び出し | ファイル |
|----------|----------|
| 予約直後の1件 | `go/livestream_handler.go` の `reserveLivestreamHandler` |
| `GET /api/livestream/:id` | `go/livestream_handler.go` の `getLivestreamHandler` |
| ライブコメントに配信を埋める | `go/livecomment_handler.go` の `fillLivecommentResponse` |
| リアクションに配信を埋める | `go/reaction_handler.go` の `fillReactionResponse` |

`livecomment_handler.go` と `reaction_handler.go` は、この1件呼び出しに替えた行だけが差分になる。コメント投稿者やリアクションしたユーザーの `fillUserResponse` は、このコミットでは1件ずつのまま。

---

## 旧 fillLivestreamResponse との違い

構造体のフィールド代入（`ID`、`Owner`、`Title`、`Tags` など）は旧関数の最後と同じ。変わっているのは、その直前で `owner` と `tags` をどこから取るか。

旧 `fillLivestreamResponse` は配信1件を受け取り、構造体を作る前にその場で SQL を出していた。

```text
SELECT * FROM users WHERE id = ?          -- owner
fillUserResponse                          -- その owner の theme と icon
SELECT * FROM livestream_tags WHERE livestream_id = ?
SELECT * FROM tags WHERE id = ?           -- タグの数だけ
```

配信が10件なら、この一連の処理が10回だった。

`fillLivestreamsResponse` は配信のスライスを受け取る。構造体を作る前に、配信から user ID と配信 ID を集める。この収集のループは SQL を出さない。同じ owner の配信が複数あっても、`uniqueInt64` で user ID を1つにする。

そのあと2つの関数が、旧関数の SQL をまとめて実行する。

| 関数 | 旧関数のどこに当たるか | 戻り値 |
|------|------------------------|--------|
| `fillUsersByIDs` | `SELECT users` と `fillUserResponse`（theme と icon） | user ID → `User` の map |
| `tagsByLivestreamID` | `livestream_tags` と、タグごとの `SELECT tags` | 配信 ID → タグ配列の map |

最後の `for` は SQL を出さない。map から `owner` と `tags` を取り、旧関数と同じフィールドで `Livestream` を作る。配信の並びは引数の順のまま。

### fillUsersByIDs の中

旧 `fillUserResponse` は owner 1人につき `themes` と `icons` を1回ずつ聞いていた。この関数は、渡された user ID 全体に対して次の3本だけ出す。

1. `SELECT * FROM users WHERE id IN (?)`  
   旧コードの `SELECT * FROM users WHERE id = ?`。返ってきた行数が user ID の数と違うときは `sql.ErrNoRows`。旧 `GetContext` は行が無いとこのエラーになるが、`IN` は欠落してもエラーにしないので、件数で同じ失敗にしている。
2. `SELECT * FROM themes WHERE user_id IN (?)`  
   旧 `fillUserResponse` の theme 取得。結果は user ID の map に置き、後のループは SQL を出さない。
3. `SELECT user_id, image FROM icons WHERE user_id IN (?)`  
   旧 `fillUserResponse` の icon 取得。ハッシュには画像バイトが要るので、`users` の `SELECT *` とは別に取る。スキャン先が `iconImageModel`。

3本のあと、user ごとに map から theme と画像を引いて `User` を作る。theme が map に無いときは `sql.ErrNoRows`。`icons` に行が無いユーザーだけ `../img/NoImage.jpg` を読み、そのバイト列は関数の中で1回だけ読む。SHA-256 は user ごとに1回。

### tagsByLivestreamID の中

旧関数は配信1件につき `livestream_tags` を1回取り、その行の数だけ `tags` を取っていた。この関数は次の2本だけ出す。

1. `SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id`  
   旧コードの `SELECT * FROM livestream_tags WHERE livestream_id = ?`。`ORDER BY id` は、旧 `SELECT *` が InnoDB で返す主キー順に合わせるための並び。
2. `SELECT * FROM tags WHERE id IN (?)`  
   旧コードの、タグ1件ずつの `SELECT * FROM tags WHERE id = ?`。紐づきから集めた tag ID の重複は `uniqueInt64` で除く。

SQL の前に、引数の配信 ID すべてへ空の `[]Tag` を入れておく。タグが0件の配信を nil のまま返すと、JSON が `null` になる。旧関数の `make([]Tag, 0)` は `[]` だった。

2本のあと、`livestream_tags` の id 順に、配信 ID ごとの配列へ `Tag` を足す。tag ID に対応する `tags` 行が無いときは `sql.ErrNoRows`。旧 `GetContext` と同じ失敗にする。

---

## 追加した関数

コード上のコメントと同じ `@params` と `@return` を書く。`@return` は戻り値で、`@result` という名前は使わない。

### fillLivestreamsResponse

旧 `fillLivestreamResponse` の一覧版。owner とタグを先に取り、構造体の組み立てだけをループする。

@params

- `ctx`: リクエストの context。キャンセルされると SELECT を中断する
- `tx`: この取得に使うトランザクション
- `livestreamModels`: 埋める配信。1件取得も長さ1のスライスで渡す。0件なら SQL を出さない

@return

- `[]Livestream`: 引数と同じ順の API 用配信。失敗時は nil
- `error`: owner またはタグの取得に失敗したとき

### fillUsersByIDs

旧関数の `SELECT users` と `fillUserResponse`（theme と icon）を、user ID の `IN` にまとめる。

@params

- `ctx`: リクエストの context。キャンセルされると SELECT を中断する
- `tx`: この取得に使うトランザクション
- `userIDs`: owner の user ID。重複は呼び出し側で除いてある。0件なら SQL を出さない

@return

- `map[int64]User`: user ID から API 用 User。失敗時は nil
- `error`: user または theme が欠けているとき、または SELECT に失敗したとき

### tagsByLivestreamID

旧関数の `livestream_tags` と、タグごとの `SELECT tags` を、配信 ID の `IN` にまとめる。処理の順番は「旧 fillLivestreamResponse との違い」の `tagsByLivestreamID の中` に書いた。

@params

- `ctx`: リクエストの context。キャンセルされると SELECT を中断する
- `tx`: この取得に使うトランザクション
- `livestreamIDs`: タグを埋める配信 ID。0件なら SQL を出さない

@return

- `map[int64][]Tag`: 配信 ID からタグ配列。タグが無い配信は空スライス。失敗時は nil
- `error`: タグ ID に対応する tags 行が無いとき、または SELECT に失敗したとき

### selectIn

`IN (?)` を ID の個数だけ広げ、その SELECT を1回実行して結果をスライスに入れる。`sqlx.In` はクエリを実行しない。`Rebind` のあと `SelectContext` が実行する。

@params

- `ctx`: リクエストの context。キャンセルされると SELECT を中断する
- `tx`: この取得に使うトランザクション
- `dest`: 結果のスキャン先。呼び出し側が型 `T` のスライスを渡す
- `query`: `IN (?)` を1つ含む SQL。実行前の文字列
- `ids`: `IN` に入れる ID。空だと `sqlx.In` がエラーになる

@return

- `error`: SQL の組み立てか実行に失敗したとき。0件はエラーにしない

### iconImageModel

関数ではない。`icons` の `user_id` と `image` だけを受ける構造体で、`SELECT user_id, image` のスキャン先。

@params

- `UserID`: `icons.user_id`
- `Image`: `icons.image`。行が無いユーザーはこの構造体に入らない

@return はない。

---

## 変更内容

一覧が0件のときは追加クエリを出さない。1件以上のとき、配信件数にかかわらず次の5本。

```sql
SELECT * FROM users WHERE id IN (?)
SELECT * FROM themes WHERE user_id IN (?)
SELECT user_id, image FROM icons WHERE user_id IN (?)
SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id
SELECT * FROM tags WHERE id IN (?)
```

- owner が同じ配信は、users / themes / icons を1ユーザーにつき1回。アイコンの SHA-256 もその回数。
- `icons` に行が無いユーザーは、従来どおり `../img/NoImage.jpg` を読む。ファイル読み込みは一覧につき1回。
- タグの並びは `livestream_tags.id` の昇順。
- user または theme が欠けているときは `sql.ErrNoRows`。単体の `GetContext` と同じ失敗にする。

---

## このコミットに含めていないもの

| 内容 | 理由 |
|------|------|
| コメント投稿者・リアクションしたユーザーの `fillUserResponse` | 配信の owner とは別の N+1。次のコミット |
| ユーザー統計、moderate | 別コミット。壁時計ではこちらが長い |
| INDEX、`SetMaxOpenConns` | 別コミット |

---

## デプロイ

前の `IN` が入った本番に、この差分を載せてからビルドする。

```sh
cd ~/webapp/go
go build -o isupipe .
sudo systemctl restart isupipe-go.service
sudo systemctl is-active isupipe-go.service
```

---

## 検証

比較対象は、この fill を載せる前の直近ベンチ（2546、視聴完了 6）。同じコマンドで1回。

```sh
./bench run --target http://pipe.u.isucon.local:8080
```

記録する項目:

- スコア（Tip 合計）
- 配信を最後まで視聴できた視聴者数
- シナリオの成功と失敗
- 最終チェックの成功と失敗

点数が 2500 前後のままなら、次はユーザー統計と moderate の壁時計を見る。このファイルに次の修正は足さない。

---

## チーム報告

```markdown
### 変更
- ファイル: `go/livestream_handler.go`
- 内容: 配信の owner / theme / icon / tags を IN にまとめた。一覧3本に加え、予約直後・GET /api/livestream/:id・livecomment・reaction の1件取得も同じ関数

- コミット: `<hash>`

### ベンチ（同一 target）
| | スコア | 視聴完了 | 備考 |
|--|--------|----------|------|
| before | 2546 | 6 | fill 反映前 |
| after | | | |

### 判断
- pass（最終チェック）:
- スコア差の要因:
```
