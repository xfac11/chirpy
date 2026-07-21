# chirpy
## What is it?
Chirpy is a social network RESTful API built using Go and their standard net/http package, SQL queries to Go functions and structures using sqlc and goose for migrations.
Use chirpy by sending GET, POST and DELETE http request to CRUD users and posts(chirps). Send http request using the docs for a full coverage of all endpoints.

## Why should I care?
Having a solid backend server for a social network is a must both for security and speed. Chirpy is easy to extend and use because of Go and has a complete documentation for all endpoints and their usage. The server also sends out errors for each endpoints to the client and logs the error in the server terminal explaining where it went wrong.

## How to install and run
1. Clone the repo into a folder of your choosing.
2. Use go install and run the executable or use go run . to run the server.
3. Create a new terminal and try some endpoints by sending http request with curl.