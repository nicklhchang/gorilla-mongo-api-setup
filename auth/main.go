package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func AuthMiddleware(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var testChainJSONStr string
		yummers := r.Cookies()
		for cKey, cValue := range yummers {
			testChainJSONStr += fmt.Sprintf("field %d: %v\n", cKey, *cValue)
		}
		/*
			Marshal encodes any Go value into a byte slice which represents JSON
			Unmarshal decodes a byte slice (which is a JSON representation) into a Go value
			Encoding and Decoding similar idea to Marshal and Unmarshal but implementation
			isn't the exact same
		*/
		json.NewEncoder(w).Encode(fmt.Sprintf("chaining works? %s", testChainJSONStr))
		/*
			purpose of chaining middlewares; pass same w and r through multiple functions (handlers).
			so call .ServeHTTP on next handler; ServeHTTP takes care of responding to a HTTP request.
		*/
		handler.ServeHTTP(w, r)
	})
}

func UserExists(searchParams bson.D, uCollection *mongo.Collection) (bool, error) {
	// can be optimised with opts refer to documentation:
	// https://pkg.go.dev/go.mongodb.org/mongo-driver/mongo#Collection.FindOne
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var user bson.M
	err := uCollection.FindOne(ctx, searchParams).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		log.Fatal(err)
		return false, err
	}
	// smoothly found: no ErrNoDocuments and unmarshaling by Decode went smooth
	return true, nil
}

// inserts doc into sessions collection. doc is the current session of authed user.
// is spawned as a goroutine and creates a session. then this goroutine will spawn
// another to delete session doc once timed out.
func CreateNewSession(channel chan<- *mongo.InsertOneResult, userInfo map[string]string,
	sCollection *mongo.Collection) {
	// in future could consider maybe reddis or just a global slice for speed
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// empty initialisation rather than nil (var sessionDocument bson.M)
	sessionDocument := make(bson.M)
	for keyField, valueUser := range userInfo {
		sessionDocument[keyField] = valueUser
	}
	newSession, err := sCollection.InsertOne(ctx, sessionDocument)
	if err != nil {
		fmt.Println("mongo error inserting new session document")
	}
	// another goroutine so no issues with blocking statement: channel <- newSession
	go DeleteSessionTimeout(sessionDocument, sCollection)
	channel <- newSession
}

// deletes session doc from mongo sessions collection. is spawned as a goroutine
// by createNewSession so that it can clean up after itself.
func DeleteSessionTimeout(session bson.M, sCollection *mongo.Collection) {
	timeoutChan := time.NewTimer(time.Duration(30 * time.Second))
	<-timeoutChan.C // halt execution of this goroutine here until timeout
	// doesn't matter how long it takes, session has to be deleted
	_, err := sCollection.DeleteOne(context.TODO(), session)
	if err != nil {
		fmt.Println("user session unable to be invalidated at this time")
	}
}

// for now almost same implementation as creating new session for authed user.
// just don't dispatch a deletion scheduled after 30 second
func CreateNewUser(channel chan<- *mongo.InsertOneResult, userInfo map[string]string,
	uCollection *mongo.Collection) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	userDocument := make(bson.M)
	for keyField, valueUser := range userInfo {
		userDocument[keyField] = valueUser
	}
	newUser, err := uCollection.InsertOne(ctx, userDocument)
	if err != nil {
		fmt.Println("mongo error inserting new user record")
	}
	channel <- newUser
}
