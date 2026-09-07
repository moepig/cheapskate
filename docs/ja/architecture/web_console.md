# Web コンソール

任意で導入できる Web コンソールは、グループ一覧、グループ詳細、schedule form、および override form を提供する。グループ詳細には、固定の所属タグ、リソース設定タグ、および読み取り専用の Describe で得た現在の状態を表示する。

server は `DEFAULT_TIMEZONE` を override 失効時刻の表示と解釈に使用する。未設定時は UTC であり、不正な値では起動に失敗する。

変更 form は Sec-Fetch-Site が存在する場合に same-origin または none だけを受け付け、Origin が存在する場合はその host と要求の Host が一致することを確認する。両ヘッダーがない要求も受け付ける。認証機能は持たず、アクセス制限は ingress で行う。条件付き書き込みの競合には HTTP 409 を返す。security header によって content、frame、referrer、および form の送信先を制限する。
