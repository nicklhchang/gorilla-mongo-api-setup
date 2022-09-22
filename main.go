package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	handlers "github.com/gorilla/handlers"
	mux "github.com/gorilla/mux"
	godotenv "github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	auth "gorilla-mongo-auth/auth"
)

var sessionCollection *mongo.Collection
var userCollection *mongo.Collection

func init() {
	// for initialising any constants/globals rest of program can access
	err := godotenv.Load("./.env")
	if err != nil {
		log.Fatal(err)
	}

	// mongoClient represents connection to mongo instance
	mongoClient, err := mongo.Connect(context.TODO(),
		options.Client().ApplyURI(os.Getenv("MONGO_URI")))
	if err != nil {
		log.Fatal(err)
	}
	// mongo instance doesn't have db called golang-tests yet
	// but creating a collection will create golang-tests db
	testDB := mongoClient.Database("golang-tests")
	collectionNames, err := testDB.ListCollectionNames(context.TODO(), bson.D{})
	if err != nil {
		log.Fatal(err)
	}
	sessionCollFound := false
	userCollFound := false
	for _, collection := range collectionNames {
		if collection == "sessions" {
			sessionCollFound = true
		}
		if collection == "users" {
			userCollFound = true
		}
	}
	if !sessionCollFound {
		err = testDB.CreateCollection(context.TODO(), "sessions")
		if err != nil {
			log.Fatal(err)
		}
	}
	if !userCollFound {
		err = testDB.CreateCollection(context.TODO(), "users")
		if err != nil {
			log.Fatal(err)
		}
	}
	sessionCollection = mongoClient.Database("golang-tests").Collection("sessions")
	userCollection = mongoClient.Database("golang-tests").Collection("users")
}

func testHelloWorld(w http.ResponseWriter, r *http.Request) {
	// set content type to json; what is being written back as a response
	// for server sent events, toggle this based on the api route frontend hits
	w.Header().Set("Content-Type", "application/json")
	test, err := sessionCollection.InsertOne(context.TODO(), bson.D{
		{Key: "testing", Value: "first-value"},
	})
	if err != nil {
		panic(err)
	}
	json.NewEncoder(w).Encode(fmt.Sprintf("hello mongo %v", test))
}

func readCountAuthedUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	authUserCount, err := sessionCollection.CountDocuments(context.TODO(), bson.D{})
	if err != nil {
		panic(err)
	}
	json.NewEncoder(w).Encode(fmt.Sprintf("hello mongo %d", authUserCount))
}

func chainMiddleware(baseHandler http.Handler,
	middlewares ...func(http.Handler) http.Handler) http.Handler {
	for _, middleware := range middlewares {
		baseHandler = middleware(baseHandler)
	}
	return baseHandler
}

/*
search for existing user
if not existing then create new user in db
then create a new session
https://stackoverflow.com/questions/43021058/golang-read-request-body-multiple-times
*/
func register(w http.ResponseWriter, r *http.Request) {
	// set http headers for when need to send response back
	w.Header().Set("Content-Type", "application/json")

	// pull out json from request body into a byte slice then into a nice map for late use
	var userInputMap map[string]string
	// fmt.Printf("body value: %v, body type: %T", (*r).Body, (*r).Body)
	userInputBytes, err := io.ReadAll(r.Body)
	if err != nil {
		json.NewEncoder(w).Encode("error: no body could be read")
	}
	err = json.Unmarshal(userInputBytes, &userInputMap)
	if err != nil {
		fmt.Println("couldn't unmarshal the byte slice representation of JSON into a map")
	}
	// construct bson document describing the user credentials passed in by body
	var userAsBSOND bson.D
	for key, value := range userInputMap {
		userAsBSOND = append(userAsBSOND, bson.E{Key: key, Value: value})
	}

	// this has to block, because whether or not user exists determines course of action
	found, err := auth.Exists(userAsBSOND, userCollection)
	if err != nil {
		fmt.Println("could not complete search for user in sessions collection")
	}
	if found {
		json.NewEncoder(w).Encode(fmt.Sprintln("user already exists"))
		fmt.Println(found, userAsBSOND)
		return
	}

	// run the two creates using goroutines to leverage context switching while InsertOne
	// is waiting (InsertOne blocks rest of Create...()); spawn goroutines for performance
	userChan := make(chan *mongo.InsertOneResult)
	sessionChan := make(chan string)
	go auth.CreateNewUser(userChan, userInputMap, userCollection)
	go auth.CreateNewSession(sessionChan, userInputMap, sessionCollection)

	// prepare response while considering potential timeout
	// if after 2 seconds both sessionID and user result not provided, timeout
	var msg, cookieName string
	for numAppended := 0; numAppended < 2; numAppended++ {
		select {
		case sess := <-sessionChan:
			msg += fmt.Sprintf("session: %v\n", string(sess))
			cookieName = fmt.Sprintf("%v", string(sess))
		case user := <-userChan:
			msg += fmt.Sprintf("user: %v\n", *user)
		case <-time.After(2 * time.Second):
			msg = "registration timed out: invalid user info or backend issue"
			numAppended = 2 // needed to break out of for loop
		}
	}
	// ask client to set a cookie, so set Set-Cookie in header according to mdn docs
	w.Header().Set("Set-Cookie", fmt.Sprintf("session-id=%s", cookieName))
	json.NewEncoder(w).Encode(msg)
}

/*
1. dispatch goroutine: search for user in user collection
2. dispatch goroutine: search for session using field "user" instead of "session"
to be considered 'logged in':
  - 1. exists and 2. not exists: create session document in db, Set-Cookie in response
  - 1. exists and 2. exists:
    if request cookie different to generated session cookie by mongo, Set-Cookie
*/
func login(w http.ResponseWriter, r *http.Request) {
	// set http headers for when need to send response back
	w.Header().Set("Content-Type", "application/json")

	// pull out json from request body into a byte slice then into a nice map for later use
	var userInputMap map[string]string
	// fmt.Printf("body value: %v, body type: %T", (*r).Body, (*r).Body)
	userInputBytes, err := io.ReadAll(r.Body)
	if err != nil {
		json.NewEncoder(w).Encode("error: no body could be read")
	}
	err = json.Unmarshal(userInputBytes, &userInputMap)
	if err != nil {
		fmt.Println("couldn't unmarshal the byte slice representation of JSON into a map")
	}
	// construct bson document describing the user credentials passed in by body
	var userAsBSOND bson.D
	for key, value := range userInputMap {
		userAsBSOND = append(userAsBSOND, bson.E{Key: key, Value: value})
	}
	fmt.Printf("user: %v", userAsBSOND)
	var sessionAsBSOND bson.D
	sessionAsBSOND = append(sessionAsBSOND, bson.E{Key: "user", Value: userInputMap["user"]})
	fmt.Printf("session: %v", sessionAsBSOND)

	/* run the two searches as specified above to leverage context switching; performance.
	e.g. FindOne blocks rest of Exists(). blocking because in waiting state (wait for mongo);
	can context switch to another goroutine */
	// user search goroutine tell login goroutine whether or not user is valid in db
	grlchangru := make(chan bool)
	// session manager goroutine sends login goroutine session string to be set in cookie
	// this can only be done if user is valid in db
	grlchangrs := make(chan string)
	// user search goroutine tell session search goroutine whether or not user is valid in db
	grschangru := make(chan bool)
	// session search goroutine sends session manager goroutine result of its search
	grssearch := make(chan string)
	// actually search for user existence in user collection
	go func() {
		userExists, err := auth.Exists(userAsBSOND, userCollection)
		if err != nil {
			log.Fatal(err)
		}
		switch userExists {
		case false:
			grlchangru <- userExists
		case true:
			grschangru <- userExists
		}
	}()
	// actually search for session existence in session collection
	go func() {
		// change this to find the session and return it, or return "" if not exists
		session, err := auth.FindSession(sessionAsBSOND, sessionCollection)
		if err != nil {
			log.Fatal(err)
		}
		grssearch <- session // don't need process anything else, so block until read out
	}()
	// coordinate result of above two searches
	go func() {
		// need to wait for below two info/guarantee before move on; blocking inevitable
		// has to block and doesn't matter the order of the two
		// the one that blocks for longest is time taken to get both results
		// instead of waiting for user search + session search
		sessionSearchResult := <-grssearch
		<-grschangru // only time receiving on this channel is if user does exist
		// session already exists for this valid user
		if len(sessionSearchResult) > 0 {
			// pump string back to login goroutine straight away
			grlchangrs <- sessionSearchResult
		} else {
			// else create new session doc in db and pump back to login goroutine
			auth.CreateNewSession(grlchangrs, userInputMap, sessionCollection)
		}
	}()

	select {
	case session := <-grlchangrs:
		// a valid session in db for a valid user: prepare write response 'logged in'
		// check if client already has cookie in request header
		ptrCookieSlice := r.Cookies()
		fmt.Printf("cookies: %v\n", ptrCookieSlice)
		var sessionIDCookie string
		for _, ptrCookie := range ptrCookieSlice {
			if (*ptrCookie).Name == "session-id" {
				sessionIDCookie = (*ptrCookie).Value
			}
		}
		// compare sessionID with session that will need to be returned
		if sessionIDCookie != session {
			// if diff, session generated by mongo will be flushed back to client
			w.Header().Set("Set-Cookie", fmt.Sprintf("session-id=%s", session))
		}
		// else assumed "Set-Cookie" header already set properly
		json.NewEncoder(w).Encode("logged in")
	case <-grlchangru: // only time something sent into grlchangru is if user not exist
		// bad news no more waiting to receive on other channels
		// rewrite Set-Cookie headers if somehow a valid session was set by above
		w.Header().Set("Set-Cookie", "session-id=oopsSomebodysUnauthenticated")
		// write straight back to client that user credentials wrong
		json.NewEncoder(w).Encode("wrong login credentials")
		return
	}
}

func sessionfind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var result bson.M
	_ = sessionCollection.FindOne(context.TODO(), bson.D{{Key: "user", Value: "tester"}}).Decode(&result)
	json.NewEncoder(w).Encode(fmt.Sprintf("result: %v, just username: %s", result, result["user"]))
}

func main() {
	router := mux.NewRouter()
	// CORS access for frontend running on port 3000
	// use http://localhost:3000 for testing
	router.Use(handlers.CORS(handlers.AllowedOrigins([]string{"*"})))

	apiV1Router := router.PathPrefix("/api/v1").Subrouter()
	v1AuthRouter := apiV1Router.PathPrefix("/auth").Subrouter()
	v1ContentRouter := apiV1Router.PathPrefix("/content").Subrouter()

	apiV1Router.HandleFunc("/test", testHelloWorld).Methods("GET")
	apiV1Router.HandleFunc("/test-session-find", sessionfind).Methods("GET")

	// /chain-test is only 'protected' route
	v1ContentRouter.
		// type http.HandlerFunc implements serveHTTP method;
		// can be passed in when parameter expected to implement http.Handler interface
		Handle("/chain-test",
			chainMiddleware(http.HandlerFunc(readCountAuthedUsers),
				auth.AuthMiddleware(sessionCollection))).
		Methods("GET")

	v1AuthRouter.HandleFunc("/register", register).Methods("POST")
	v1AuthRouter.HandleFunc("/login", login).Methods("POST")

	log.Fatal(http.ListenAndServe(":3001", router))
}
