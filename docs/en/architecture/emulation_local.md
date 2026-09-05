# Local emulation

`make dev` starts the local AWS emulator, creates the DynamoDB table, seeds tagged ECS services, creates a sample schedule group, and starts the web console on `127.0.0.1:8080`.

The emulator is also used by integration and image tests. Some control-plane behavior differs from AWS; adapter unit tests cover API request selection and validation independently.
