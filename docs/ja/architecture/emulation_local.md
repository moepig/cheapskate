# ローカルエミュレーション

`make dev` はローカル AWS emulator を起動し、DynamoDB table とタグ付き ECS service を作成し、サンプルの schedule group を保存して、Web コンソールを `127.0.0.1:8080` で起動する。

同じ emulator を integration test と image test でも使用する。AWS と異なる control plane の動作は、adapter の unit test で API request の選択と検証を個別に確認する。
