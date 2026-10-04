# Web コンソール

任意で導入できる Web コンソールは、グループ一覧、グループ詳細、schedule form、および override form を提供する。グループ詳細には、固定の所属タグ、リソース設定タグ、および読み取り専用の Describe で得た現在の状態を表示する。

server は `DEFAULT_TIMEZONE` を override 失効時刻の表示と解釈に使用する。未設定時は UTC であり、不正な値では起動に失敗する。

全要求で、ルーティングと状態へのアクセスの前に Host をサーバ生成時に固定した許可リストと照合する。ポートを含め、大文字と小文字を区別せず完全一致で判定する。空の許可リストでは全要求を拒否する。これは、未許可のドメインから DNS rebinding によって Web コンソールへアクセスすることを防ぐためである。実行ファイルは ALLOWED_HOSTS で許可リストを設定し、既定値は待ち受けポート付きのループバックホストとする。

変更 form は追加で Sec-Fetch-Site が存在する場合に same-origin または none だけを受け付け、Origin が存在する場合はその host と要求の Host が一致することを確認する。両ヘッダーがない要求にも許可された Host を必要とする。認証機能は持たず、アクセス制限は ingress で行う。条件付き書き込みの競合には HTTP 409 を返す。security header によって content、frame、referrer、および form の送信先を制限する。
