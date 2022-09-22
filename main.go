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

func readCountAuthedUsers(w http.ResponseWriter, r *http.Request) {
	// set content type to json; what is being written back as a response
	// for server sent events, toggle this based on the api route frontend hits
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
	var sessionAsBSOND bson.D
	sessionAsBSOND = append(sessionAsBSOND, bson.E{Key: "user", Value: userInputMap["user"]})

	/* run the two searches as specified above to leverage context switching; performance.
	e.g. FindOne is mongo api call; puts goroutine (the one calling Exists()) in waiting state.
	can context switch to another goroutine (the one calling FindSession) */
	// user search goroutine tell login goroutine whether or not user is valid in db
	grlchangru := make(chan bool)
	// session goroutine sends login goroutine the session string (for cookie use)
	grlchangrs := make(chan string)
	// search for user existence in user collection
	go func() {
		userExists, err := auth.Exists(userAsBSOND, userCollection)
		if err != nil {
			log.Fatal(err)
		}
		grlchangru <- userExists
	}()
	// search for session existence in session collection
	go func() {
		session, err := auth.FindSession(sessionAsBSOND, sessionCollection)
		if err != nil {
			log.Fatal(err)
		}
		grlchangrs <- session // not process anything else, so block until elsewhere read out
	}()

	var msg, cookie string
	for numReceives := 0; numReceives < 2; numReceives++ {
		select {
		case session := <-grlchangrs:
			if len(session) > 0 { // found a valid session but not sure if user valid
				cookie = session
			} else {
				// make compatible with current CreateNewSession() which shares result via channel
				chanSString := make(chan string)
				// even if context switch back to this login goroutine while waiting for InsertOne()
				// rest of this function blocked by cookie = <-chanSString
				go auth.CreateNewSession(chanSString, userInputMap, sessionCollection)
				cookie = <-chanSString
			}
			msg = "logged in" // now has a valid session but if user not exist msg gets overwritten
		case userExists := <-grlchangru:
			if !userExists {
				// rewrite Set-Cookie headers if somehow a valid session was set by above
				// prevent bug if there is a session document for a user but user not registered
				cookie = "session-id=oopsSomebodysUnauthenticated"
				msg = "wrong login credentials"
				// skip straight out of for loop: prevent any overwriting from other case (safety)
				numReceives = 2
			}
		}
	}
	// doesn't matter if client already has cookie set in header, overwrite
	w.Header().Set("Set-Cookie", fmt.Sprintf("session-id=%s", cookie))
	json.NewEncoder(w).Encode(msg)
}

func main() {
	router := mux.NewRouter()
	// CORS access for frontend running on port 3000
	// use http://localhost:3000 for testing
	router.Use(handlers.CORS(handlers.AllowedOrigins([]string{"*"})))

	apiV1Router := router.PathPrefix("/api/v1").Subrouter()
	v1AuthRouter := apiV1Router.PathPrefix("/auth").Subrouter()
	v1ContentRouter := apiV1Router.PathPrefix("/content").Subrouter()

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
