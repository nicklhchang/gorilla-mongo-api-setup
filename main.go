package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

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
search for existing user: lookForUser()
if not existing then create new user in db: go createNewUser()
then create a new session: go createNewSession()
https://stackoverflow.com/questions/43021058/golang-read-request-body-multiple-times
*/
func register(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// fmt.Printf("body value: %v, body type: %T", (*r).Body, (*r).Body)

	// pull out json from request body into a byte slice then into a nice map
	var userInputMap map[string]string
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
	found, err := auth.UserExists(userAsBSOND, userCollection)
	if err != nil {
		fmt.Println("could not complete search for user in sessions collection")
	}
	if found {
		json.NewEncoder(w).Encode(fmt.Sprintln("user already exists"))
		fmt.Println(found, userAsBSOND)
		return
	}

	// run the two creates using goroutines to leverage context switching while
	// the asynchronous InsertOne is blocking (spawn goroutines for performance)
	// userChan := make(chan *mongo.InsertOneResult)
	sessionChan := make(chan *mongo.InsertOneResult)
	// go createNewUser(userChan, userInputMap)
	go auth.CreateNewSession(sessionChan, userInputMap, sessionCollection)
	json.NewEncoder(w).Encode(fmt.Sprintln(*(<-sessionChan)))
}

func main() {
	router := mux.NewRouter()
	// CORS access for frontend running on port 3000
	router.Use(handlers.CORS(handlers.AllowedOrigins([]string{"*"})))

	apiV1Router := router.PathPrefix("/api/v1").Subrouter()
	apiV1Router.HandleFunc("/test", testHelloWorld).Methods("GET")
	// apiV1Router.Handle("/chain-test", testChain(http.HandlerFunc(testHelloWorld))).Methods("GET")
	// /chain-test is only 'protected' route
	apiV1Router.
		// type http.HandlerFunc implements serveHTTP method; can be passed in when parameter expected
		// to implement http.Handler interface
		Handle("/chain-test", chainMiddleware(http.HandlerFunc(readCountAuthedUsers), auth.AuthMiddleware)).
		Methods("GET")
	apiV1Router.HandleFunc("/register", register).Methods("POST")

	log.Fatal(http.ListenAndServe(":3001", router))
}
