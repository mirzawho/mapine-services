const mongoose = require('mongoose');

const placeSchema = new mongoose.Schema(
    {
      osm_id: String,
      category: String,
      tags: Object,
      location: {
        type: { type: String, enum: ["Point"], default: "Point" },
        coordinates: { type: [Number], required: true }
      },
      lat: Number,
      lon: Number,
      score: { type: Number, default: 0 },
      address: String,
      neighbourhood: String,
      city: String,
      county: String,
      state: String,
      country: String,
      title: String,
      description: String
    },
    { timestamps: true }
  );
  
  placeSchema.index({ category: 1 });
  placeSchema.index({ location: "2dsphere" });
  placeSchema.index({ "location.coordinates": 1 }, { unique: true });
  
  const Place = mongoose.model("place", placeSchema);

  module.exports = {
    place: Place
  }