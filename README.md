# CBPROXY

- Simple proxy that forwards request based on  predefined URL's 
- The Remote url can be selected with `"proxy_condition"` in body of request

```
curl --request GET \
  --url http://localhost:1330/ \
  --header 'content-type: application/json' -v \
  --data '{
    "proxy_condition": "b"
  }'
```

- CBProxy will find  matching URL and proxy the request
- CBproxy implement Circuit breaker(CB) pattern
    - On startup CB will be in closed state and forward all the requests
    - After 3(configurable) consequtive failures CB will change the state to `Open` and start 20 seconds timer, 
    - During this time all requests will fail immideately
    - After 20 seconds timeout , CB will change the state to `HalfOpen` which will allow single request to pass through
    - If the request successful , it will Close the CB other Open it and start another 20 seconds time cycle