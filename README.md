Basic configuration for authenticating http requests. Done by chaining middleware and using server side session-based authentication with MongoDB. 

Visits to protected routes will require a cookie in http header to be checked against sessions saved in MongoDB.
