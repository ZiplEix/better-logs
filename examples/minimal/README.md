# How to run the minimal example

From this directory, run:

```bash
# 1. start postgres
docker compose up -d

# 2. run the example server
go run ./main.go
```

then:
```bash
curl http://localhost:8080/hello
# -> ok
```

And in postgres you should see the logs:
```sql
SELECT ts, req_id, raw
FROM logs
ORDER BY ts DESC
LIMIT 10;
```
