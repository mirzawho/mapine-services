const axios = require('axios');

const { NOMINATIM_URL } = process.env;

module.exports = {
    async fromLatLng(lat, lng, zoom = 15, language = 'فارسی') {
        try {
            let lang = '';

            if (language === 'فارسی') {
                lang = 'fa';
            } else if (language === 'english') {
                lang = 'en';
            }

            // Use Nominatim API (OpenStreetMap) for reverse geocoding - Free service
            const url = `${NOMINATIM_URL}/reverse?format=json&lat=${lat}&lon=${lng}&zoom=${zoom}&addressdetails=1&accept-language=${lang}`;

            const response = await axios.get(url, {
                headers: {
                    'User-Agent': 'OhMyAPI-Services/1.0.0'
                }
            });

            if (response.status !== 200 || !response.data) {
                return {
                    status: 404,
                    message: 'Address not found for these coordinates'
                }
            }

            const data = response.data;
            const address = data.address || {};

            // Format the response with Persian/Farsi support
            const formattedAddress = {
                coordinates: {
                    lat: parseFloat(data.lat),
                    lng: parseFloat(data.lon)
                },
                address: data.display_name,
                house_number: address.house_number || null,
                road: address.road || null,
                neighbourhood: address.neighbourhood || address.suburb || null,
                city: address.city || address.town || address.village || null,
                county: address.county || null,
                state: address.state || null,
                country: address.country || null,
                country_code: address.country_code || null,
                postcode: address.postcode || null,
                place_type: data.type || null,
                importance: data.importance || null,
                place_rank: data.place_rank || null,
                boundingbox: data.boundingbox ? {
                    south: parseFloat(data.boundingbox[0]),
                    north: parseFloat(data.boundingbox[1]),
                    west: parseFloat(data.boundingbox[2]),
                    east: parseFloat(data.boundingbox[3])
                } : null
            };

            return {
                status: 200,
                meta: {},
                address: {
                    ...formattedAddress,
                    address: formattedAddress.address.replace(formattedAddress.postcode, '').trim(),
                    city: formattedAddress.city?.replace('شهر', '').trim() ?? null,
                    county: formattedAddress.county?.replace('شهرستان', '').trim() ?? null,
                    state: formattedAddress.state?.replace('استان', '').trim() ?? null,
                }
            }
        } catch (error) {
            console.error('Reverse geocoding error:', error.message);
            return {
                status: 500,
                message: 'Internal server error'
            }
        }
    },
    async fromAddress(address, city, limit = 10, language = 'فارسی', country = 'ایران') {
        try {
            let lang = '';

            if (language === 'فارسی') {
                lang = 'fa';
            } else if (language === 'english') {
                lang = 'en';
            }

            let addr = [country ?? '', city ?? '', address].filter(Boolean).join(', ').trim();

            // Use Nominatim API for forward geocoding - Free service
            const url = `${NOMINATIM_URL}/search?format=json&q=${encodeURIComponent(addr)}&limit=${limit}&addressdetails=1&accept-language=${lang}`;

            const response = await axios.get(url, {
                headers: {
                    'User-Agent': 'OhMyAPI-Services/1.0.0'
                }
            });

            if (response.status !== 200 || !response.data || response.data.length === 0) {
                return {
                    status: 404,
                    message: 'No coordinates found for this address'
                }
            }

            const results = response.data.map(item => {
                const addressComponents = item.address || {};

                let address = item.display_name;

                if (addressComponents.postcode) {
                    address = address.replace(addressComponents.postcode, '').trim();
                }

                return {
                    coordinates: {
                        lat: parseFloat(item.lat),
                        lng: parseFloat(item.lon)
                    },
                    address,
                    house_number: addressComponents.house_number || null,
                    road: addressComponents.road || null,
                    neighbourhood: addressComponents.neighbourhood || addressComponents.suburb || null,
                    city: addressComponents.city || addressComponents.town || addressComponents.village || null,
                    county: addressComponents.county || null,
                    state: addressComponents.state || null,
                    country: addressComponents.country || null,
                    country_code: addressComponents.country_code || null,
                    postcode: addressComponents.postcode || null,
                    place_type: item.type || null,
                    importance: item.importance || null,
                    place_rank: item.place_rank || null,
                    boundingbox: item.boundingbox ? {
                        south: parseFloat(item.boundingbox[0]),
                        north: parseFloat(item.boundingbox[1]),
                        west: parseFloat(item.boundingbox[2]),
                        east: parseFloat(item.boundingbox[3])
                    } : null
                };
            });

            return {
                status: 200,
                meta: {
                    total: results.length,
                    query: addr,
                },
                locations: results,
            }
        } catch (error) {
            console.error('Forward geocoding error:', error.message);
            return {
                status: 500,
                message: 'Internal server error'
            }
        }
    },
    async search(lat, lon, zoom, category) {
        try {
            lat = Number((lat).toFixed(6));
            lon = Number((lon).toFixed(6));

            let places = [];

            const resultOfTokens = await this.getPOITokens({
                lat,
                lon,
                category,
                zoom,
            });

            if (resultOfTokens.length === 0) {
                return {
                    status: 400,
                    message: 'EMPTY_TOKENS'
                }
            }

            const resultOfPlaces = await this.getPlacesFromPOITokens(
                resultOfTokens,
            );

            for (let item of resultOfPlaces) {
                let lat = item["geometry"]["coordinates"][0];
                let lon = item["geometry"]["coordinates"][1];

                const document = {
                    id: item['token'],
                    site: "balad.ir",
                    schema: "map:place",
                    url: `https://balad.ir/bundles#$16/${lat}/${lon}`,
                    data: {
                        geometry: {
                            lat,
                            lon,
                        },
                        distance: this.calculateDistance(lat, lon, lat, lon), // Add distance
                        distance_unit: "km",
                        category: category,
                        name: item["name"],
                        address: item["address"],
                        city: null,
                        province: null,
                        thumbnail: item["thumbnail"],
                        gallery: item["images"]
                            ? item["images"].map((item) => item["full"])
                            : [],
                        telephone:
                            item["telephone"] && item["telephone"].length !== 0
                                ? item["telephone"]
                                : null,
                        rating: {
                            score: item["rating"] ? item["rating"]["score"] : 0,
                            count: item["rating"] ? item["rating"]["count"] : 0,
                        },
                    },
                    version: 1,
                    createdAt: Date.now(),
                    updatedAt: Date.now(),
                };

                places.push(document);
            }

            return {
                status: 200,
                meta: {
                    total: places.length,
                },
                places,
            };
        } catch (e) {
            console.error(e);

            return {
                code: 500
            }
        }
    },
    /**
     * Calculate distance between two points using Haversine formula
     * @param {number} lat1 First point latitude
     * @param {number} lon1 First point longitude
     * @param {number} lat2 Second point latitude
     * @param {number} lon2 Second point longitude
     * @returns {number} Distance in kilometers
     */
    calculateDistance(lat1, lon1, lat2, lon2) {
        const R = 6371; // Earth's radius in kilometers
        const dLat = (lat2 - lat1) * Math.PI / 180;
        const dLon = (lon2 - lon1) * Math.PI / 180;
        const a =
            Math.sin(dLat / 2) * Math.sin(dLat / 2) +
            Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) *
            Math.sin(dLon / 2) * Math.sin(dLon / 2);
        const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
        return +(R * c).toFixed(2); // Round to 2 decimal places
    },
    async getPOITokens({ zoom, lat, lon, category }) {
        try {
            const url = `https://search.raah.ir/v4/bundle-search/?camera=${lat},${lon}&format=json&slug=${category}&zoom=${zoom}`;

            const result = await axios.get(url);

            return result.data["poi-tokens"];
        } catch (e) {
            return [];
        }
    },
    /**
     *
     * @param {string[]} tokens
     * @returns {Promise<*>}
     */
    async getPlacesFromPOITokens(tokens) {
        const url = `https://poi.raah.ir/web/v4/preview-bulk/${tokens.join(",")}`;

        const result = await axios.get(url);

        return result.data.items;
    },
};