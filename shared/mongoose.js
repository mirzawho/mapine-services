const mongoose = require("mongoose");

const {
    MONGO_USERNAME,
    MONGO_PASSWORD,
    MONGO_HOST,
    MONGO_PORT,
    MONGO_DATABASE
} = process.env;


const connect = () => {
    const MONGO_URL = `mongodb://${MONGO_USERNAME}:${MONGO_PASSWORD}@${MONGO_HOST}:${MONGO_PORT}/${MONGO_DATABASE}`;
    
    console.log(`Connecting mongodb to ${MONGO_URL}`);

   return mongoose.connect(MONGO_URL, { 'authSource': 'admin', family: 4 }).then(() => {
        console.log('Connected to mongodb');
    }).catch((error) => {
        console.error('Failed to connect to mongodb');
        console.error(error);
        process.exit(1);
    });
}

const schema = new mongoose.Schema({}, {
    strict: false, toJSON: {
        transform: function (doc, ret) {
            ret.id = ret._id;
            delete ret.__v;
            delete ret._id;
        }
    }
});

const collection = (name, customSchema) => {
    return mongoose.model(name, customSchema ?? schema);
};

module.exports = {
    connect,
    schema,
    collection,
    mongoose,
};